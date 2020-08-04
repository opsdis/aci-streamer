// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// Copyright 2020 Opsdis AB

package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/segmentio/ksuid"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"net/http"
	"os"
	"strconv"
	"time"
)

var version = "undefined"

func main() {
	flag.Usage = func() {
		fmt.Printf("Usage of %s:\n", ExporterName)
		fmt.Printf("Version %s\n", version)
		flag.PrintDefaults()
	}

	SetDefaultValues()

	flag.Int("p", viper.GetInt("port"), "The port to start on")
	logFile := flag.String("logfile", viper.GetString("logfile"), "Set log file, default stdout")
	logFormat := flag.String("logformat", viper.GetString("logformat"), "Set log format to text or json, default json")

	configFile := flag.String("config", viper.GetString("config"), "Set configuration file, default config.yaml")
	usage := flag.Bool("u", false, "Show usage")
	writeConfig := flag.Bool("default", false, "Write default config")
	fabric := flag.String("fabric", viper.GetString("fabric"), "The fabric to use in the config")

	flag.Parse()

	log.SetFormatter(&log.JSONFormatter{})
	if *logFormat == "text" {
		log.SetFormatter(&log.TextFormatter{})
	}

	viper.SetConfigName(*configFile) // name of config file (without extension)
	viper.SetConfigType("yaml")      // REQUIRED if the config file does not have the extension in the name

	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.aci-streamer")
	viper.AddConfigPath("/usr/local/etc/aci-streamer")
	viper.AddConfigPath("/etc/aci-streamer")

	if *usage {
		flag.Usage()
		os.Exit(0)
	}

	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Println(err)
		}
		log.SetOutput(f)
	}

	if *writeConfig {
		err := viper.WriteConfigAs("./aci_streamer_default_config.yaml")
		if err != nil {
			log.Error("Can not write default config file - ", err)
		}
		os.Exit(0)
	}

	// Find and read the config file
	err := viper.ReadInConfig()
	if err != nil {
		log.Error("Configuration file not valid ", err)
		os.Exit(1)
	}

	var streams = Streams{}
	err = viper.UnmarshalKey("streams", &streams)
	if err != nil {
		log.Error("Unable to decode streams into struct - ", err)
		os.Exit(1)
	}

	username := viper.GetString(fmt.Sprintf("fabrics.%s.username", *fabric))
	password := viper.GetString(fmt.Sprintf("fabrics.%s.password", *fabric))
	apicControllers := viper.GetStringSlice(fmt.Sprintf("fabrics.%s.apic", *fabric))

	fabricConfig := Fabric{Username: username, Password: password, Apic: apicControllers}
	ctx := context.TODO()
	connection := newAciConnction(ctx, fabricConfig, streams)

	// Create a Prometheus histogram for response time of the exporter
	responseTime := promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    MetricsPrefix + "request_duration_seconds",
		Help:    "Histogram of the time (in seconds) each request took to complete.",
		Buckets: []float64{0.001, 0.005, 0.010, 0.020, 0.100, 0.200, 0.500},
	},
		[]string{"url", "status"},
	)
	go startStreamer(connection, streams)
	http.Handle("/alive",
		logcall(promMonitor(http.HandlerFunc(alive), responseTime, "/alive")))
	http.Handle("/metrics", promhttp.HandlerFor(
		prometheus.DefaultGatherer,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
		},
	))

	log.Info(fmt.Sprintf("%s starting on port %d", ExporterName, viper.GetInt("port")))
	s := &http.Server{
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		Addr:         ":" + strconv.Itoa(viper.GetInt("port")),
	}
	log.Fatal(s.ListenAndServe())
}

func startStreamer(connection *AciConnection, streams Streams) {
	err := connection.login()
	if err != nil {
		os.Exit(1)
	}
	fabricName, _ := connection.getFabricName()
	// TODO add channel to listen to if websocket fail
	ch := make(chan int)
	go connection.startWebSocket(fabricName, ch)
	time.Sleep(1 * time.Second)

	subIds := make(map[string]string)

	for k, v := range streams {
		subscriptionId, _ := connection.subscribe(v.ClassName, v.QueryParameter)
		subIds[subscriptionId] = k
	}

	connection.activeSubscribtions(subIds)

	// Enters refresh loop
	// TODO add handling to restart everything if any of below fail
	for {
		select {
		case msg1 := <-ch:
			if msg1 == 0 {
				log.Info(fmt.Sprintf("Re-subscribe"))
				subIds = make(map[string]string)
				for k, v := range streams {
					subscriptionId, _ := connection.subscribe(v.ClassName, v.QueryParameter)
					subIds[subscriptionId] = k
				}
				connection.activeSubscribtions(subIds)
			}
		case <-time.After(time.Second * 30):
			log.Info(fmt.Sprintf("Start refresh"))
		}

		log.Info(fmt.Sprintf("Refresh session"))
		err = connection.sessionRefresh()
		if err != nil {
			log.Error("Session refresh failed - ", err)
		}

		for k, v := range subIds {
			log.Info(fmt.Sprintf("Refresh id %s for %s", k, v))
			err = connection.subscriptionRefresh(k)
			if err != nil {
				log.Error("Subscription refresh failed - ", err)
			}
		}
	}
}

func alive(w http.ResponseWriter, r *http.Request) {

	var alive = fmt.Sprintf("Alive!\n")
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(alive)))
	lrw := loggingResponseWriter{ResponseWriter: w}
	lrw.WriteHeader(200)

	w.Write([]byte(alive))
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	length     int
}

func logcall(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		start := time.Now()

		lrw := loggingResponseWriter{ResponseWriter: w}
		requestid := nextRequestID()

		ctx := context.WithValue(r.Context(), "requestid", requestid)
		next.ServeHTTP(&lrw, r.WithContext(ctx)) // call original

		w.Header().Set("Content-Length", strconv.Itoa(lrw.length))
		log.WithFields(log.Fields{
			"method": r.Method,
			"uri":    r.RequestURI,
			//"endpoint":  endpoint,
			"status":    lrw.statusCode,
			"length":    lrw.length,
			"requestid": requestid,
			"exec_time": time.Since(start).Microseconds(),
		}).Info("api call")
	})

}

func nextRequestID() ksuid.KSUID {
	return ksuid.New()
}

func promMonitor(next http.Handler, ops *prometheus.HistogramVec, endpoint string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		start := time.Now()

		lrw := loggingResponseWriter{ResponseWriter: w}

		next.ServeHTTP(&lrw, r) // call original

		response := time.Since(start).Seconds()

		ops.With(prometheus.Labels{"url": endpoint, "status": strconv.Itoa(lrw.statusCode)}).Observe(response)
	})
}
