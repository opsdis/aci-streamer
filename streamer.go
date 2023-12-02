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
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/segmentio/ksuid"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
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
	output := flag.String("output", viper.GetString("output"), "The output file, default stdout")

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
			fmt.Printf("Can not write default config file - %s\n", err)
		}
		os.Exit(0)
	}

	// Find and read the config file
	err := viper.ReadInConfig()
	if err != nil {
		fmt.Printf("Configuration file not valid - %s\n", err)
		os.Exit(1)
	}

	var streams = Streams{}
	err = viper.UnmarshalKey("streams", &streams)
	if err != nil {
		fmt.Printf("Unable to decode streams into struct - %s\n", err)
		os.Exit(1)
	}

	username := viper.GetString(fmt.Sprintf("fabrics.%s.username", *fabric))
	password := viper.GetString(fmt.Sprintf("fabrics.%s.password", *fabric))
	apicControllers := viper.GetStringSlice(fmt.Sprintf("fabrics.%s.apic", *fabric))

	fabricConfig := Fabric{Name: *fabric, Username: username, Password: password, Apic: apicControllers}
	ctx := context.TODO()
	connection := newAcidConnection(ctx, fabricConfig, streams, *output)

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

	log.WithFields(log.Fields{
		"port": viper.GetInt("port"),
		"name": ExporterName,
	}).Info("starting")
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
	fabricACIName, _ := connection.getFabricACIName()

	ch := make(chan string)

	go connection.startWebSocket(fabricACIName, ch)

	subIds := make(map[string]string)

	for {
		select {
		case fromWS := <-ch:
			if fromWS == "failed" {
				// The websocket has for some reason failed and must be restarted
				log.WithFields(log.Fields{
					"requestid": connection.ctx.Value("requestid"),
					"fabric":    fabricACIName,
				}).Info("websocket restarted on failed")

				go connection.startWebSocket(fabricACIName, ch)

			}
			if fromWS == "started" {
				// The websocket has been started and is ready
				log.WithFields(log.Fields{
					"requestid": connection.ctx.Value("requestid"),
					"fabric":    fabricACIName,
				}).Info(fmt.Sprintf("websocket ready for subscribtion"))

				subIds = make(map[string]string)
				for k, v := range streams {
					subscriptionId, _ := connection.subscribe(v.ClassName, v.QueryParameter)
					subIds[subscriptionId] = k

				}
				connection.activeSubscriptions(subIds)

			}
		case <-time.After(time.Second * 30):
			log.WithFields(log.Fields{
				"requestid": connection.ctx.Value("requestid"),
				"fabric":    fabricACIName,
				"interval":  30,
			}).Info(fmt.Sprintf("start subscribtion refresh on interval"))
		}
		/*
			log.WithFields(log.Fields{
				"requestid": connection.ctx.Value("requestid"),
				"fabric":    fabricACIName,
			}).Info(fmt.Sprintf("Refresh subscribtion"))
		*/
		// this is login again
		err = connection.sessionRefresh()
		if err != nil {
			log.WithFields(log.Fields{
				"requestid": connection.ctx.Value("requestid"),
				"fabric":    fabricACIName,
				"error":     err,
			}).Error("session refresh failed")
			err = connection.login()
			if err != nil {
				log.WithFields(log.Fields{
					"requestid": connection.ctx.Value("requestid"),
					"fabric":    fabricACIName,
					"error":     err,
				}).Error("session login failed")
			}
		}

		for k, v := range subIds {
			if v == "" {
				// Must subscribe again since lost id
				log.WithFields(log.Fields{
					"requestid":      connection.ctx.Value("requestid"),
					"fabric":         fabricACIName,
					"subscriptionid": k,
				}).Warn(fmt.Sprintf("empty stream name"))
				delete(subIds, k)
				subscriptionId, _ := connection.subscribe(streams[v].ClassName, streams[v].QueryParameter)
				subIds[subscriptionId] = v
			}

			// This websocket refresh
			err = connection.subscriptionRefresh(k)
			if err != nil {
				log.WithFields(log.Fields{
					"requestid":      connection.ctx.Value("requestid"),
					"fabric":         fabricACIName,
					"subscriptionid": k,
					"stream":         v,
					"error":          err,
				}).Error("subscription refresh failed, will be reconnected next iteration")
			} else {
				log.WithFields(log.Fields{
					"requestid":      connection.ctx.Value("requestid"),
					"fabric":         fabricACIName,
					"subscriptionid": k,
					"stream":         v,
				}).Info(fmt.Sprintf("refresh subscribtion"))
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
			"method":    r.Method,
			"uri":       r.RequestURI,
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
