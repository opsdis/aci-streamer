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
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/gorilla/websocket"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"strings"

	//"github.com/tidwall/sjson"
	"github.com/umisama/go-regexpcache"
	"io/ioutil"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)


// AciConnection is the connection object
type AciConnection struct {
	ctx                 context.Context
	fabricConfig        Fabric
	activeController    *int
	URLMap              map[string]string
	Headers             map[string]string
	Client              HTTPClient
	cookieValue         *string
	activeSubscribtionIds map[string]string
}

func newAciConnction(ctx context.Context, fabricConfig Fabric) *AciConnection {
	// Empty cookie jar
	jar, _ := cookiejar.New(nil)

	var httpClient = HTTPClient{
		InsecureHTTPS:       viper.GetBool("HTTPClient.insecureHTTPS"),
		Timeout:             viper.GetInt("HTTPClient.timeout"),
		Keepalive:           viper.GetInt("HTTPClient.keepalive"),
		Tlshandshaketimeout: viper.GetInt("HTTPClient.tlshandshaketimeout"),
		cookieJar:           jar,
	}

	var headers = make(map[string]string)
	headers["Content-Type"] = "application/json"

	urlMap := make(map[string]string)

	urlMap["login"] = "/api/mo/aaaLogin.xml"
	urlMap["logout"] = "/api/mo/aaaLogout.xml"
	urlMap["fabric_name"] = "/api/mo/topology/pod-1/node-1/av.json"

	return &AciConnection{
		ctx:              ctx,
		fabricConfig:     fabricConfig,
		activeController: new(int),
		URLMap:           urlMap,
		Headers:          headers,
		Client:       httpClient,
		cookieValue:  new(string),
		activeSubscribtionIds: make(map[string]string),
	}
}

func (c AciConnection) login() error {
	for i, controller := range c.fabricConfig.Apic {
		_, status, err, jars := c.doPostXML(fmt.Sprintf("%s%s", controller, c.URLMap["login"]),
			[]byte(fmt.Sprintf("<aaaUser name=%s pwd=%s/>", c.fabricConfig.Username, c.fabricConfig.Password)))
		if err != nil || status != 200 {

			err = fmt.Errorf("failed to login to %s, try next apic", controller)

			log.Error(err)
		} else {
			*c.activeController = i
			log.WithFields(log.Fields{
				"requestid": c.ctx.Value("requestid"),
			}).Info("Using apic %s", controller)
			c.printCookie(jars)
			*c.cookieValue = jars[0].Value
			return nil
		}
	}
	return fmt.Errorf("failed to login to any apic controllers")

}

func (c AciConnection) printCookie(jars []*http.Cookie) {

	var cookieNum int = len(jars)
	log.Printf("cookieNum=%d", cookieNum)
	for i := 0; i < cookieNum; i++ {
		var curCk *http.Cookie = jars[i]
		//log.Printf("curCk.Raw=%s", curCk.Raw)
		log.Printf("Cookie [%d]", i)
		log.Printf("Name\t=%s", curCk.Name)
		log.Printf("Value\t=%s", curCk.Value)
		log.Printf("Path\t=%s", curCk.Path)
		log.Printf("Domain\t=%s", curCk.Domain)
		log.Printf("Expires\t=%s", curCk.Expires)
		log.Printf("RawExpires=%s", curCk.RawExpires)
		log.Printf("MaxAge\t=%d", curCk.MaxAge)
		log.Printf("Secure\t=%t", curCk.Secure)
		log.Printf("HttpOnly=%t", curCk.HttpOnly)
		log.Printf("Raw\t=%s", curCk.Raw)
		log.Printf("Unparsed=%s", curCk.Unparsed)
	}

}

func (c AciConnection) sessionRefresh() bool {
	c.get(fmt.Sprintf("%s/api/aaaRefresh.json", c.fabricConfig.Apic[*c.activeController]))
	return true
}


func (c AciConnection) subscriptionRefresh(subscriptionId string) bool {
	c.get(fmt.Sprintf("%s/api/subscriptionRefresh.json?id=%s", c.fabricConfig.Apic[*c.activeController], subscriptionId))
	return true
}

func (c AciConnection) logout() bool {
	_, status, err, _ := c.doPostXML(fmt.Sprintf("%s%s", c.fabricConfig.Apic[*c.activeController], c.URLMap["logout"]),
		[]byte(fmt.Sprintf("<aaaUser name=%s/>", c.fabricConfig.Username)))
	if err != nil || status != 200 {
		log.WithFields(log.Fields{
			"requestid": c.ctx.Value("requestid"),
		}).Error(err)
		return false
	}
	return true
}

// TODO spara class relationen till subscription - användas när man plockar ut stömmen från websocket
func (c AciConnection) subscribe(class string, query string) (string, error) {
	var err error
	var data []byte
	if query == "" {
		data, err = c.get(fmt.Sprintf("%s/api/class/%s.json?page=0&page-size=1&subscription=yes", c.fabricConfig.Apic[*c.activeController], class))
	} else {
		data, err = c.get(fmt.Sprintf("%s/api/class/%s.json%s&page=0&page-size=1&subscription=yes", c.fabricConfig.Apic[*c.activeController], class, query))
	}

	if err != nil {
		log.WithFields(log.Fields{
			"requestid": c.ctx.Value("requestid"),
		}).Error(fmt.Sprintf("Class request %s failed - %s.", class, err))
		return "", err
	}
	subscriptionId := gjson.Get(string(data), "subscriptionId").Str
	return subscriptionId, nil
}

func (c AciConnection) startWebSocket(fabricName string) {
	//u := url.URL{Scheme: "wss", Host: c.fabricConfig.Apic[*c.activeController], Path: "/socket" + *c.cookieValue}
	u := url.URL{Scheme: "wss", Host: "sandboxapicdc.cisco.com:443", Path: "/socket" + *c.cookieValue}
	log.Info("connecting to %s", u.String())
	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	config := tls.Config{RootCAs: rootCAs, InsecureSkipVerify: true}

	wsHeaders := http.Header{
		"Origin": {u.Host},
		"Sec-WebSocket-Extensions": {"permessage-deflate; client_max_window_bits, x-webkit-deflate-frame"},
	}

	d := websocket.Dialer{TLSClientConfig: &config}
	wc, _, err := d.Dial(u.String(), wsHeaders)

	if err != nil {
		log.Fatal("dial:", err)
	}

	defer wc.Close()

	for {
		_, mesg, err := wc.ReadMessage()
		if err != nil {
			log.Info("read:", err)
		}
		// Flytta til config
		// Get subscriptionID -> vilken konfig

		c.reciver(fabricName, mesg)
	}


}
func (c AciConnection) activeSubscribtions(ids map[string]string) {
	for k, v := range ids {
		c.activeSubscribtionIds[k] = v
	}
}

func (c AciConnection) reciver(fabricName string, mesg []byte) {

	subscribtionName := c.getSubscribersName(mesg)

	json := gjson.Get(string(mesg), "imdata.0.faultInst.attributes")
	descr := gjson.Get(json.Raw, "descr").Str
	dn := gjson.Get(json.Raw, "dn").Str
	//t, _ := time.Parse("2020-07-30T23:14:40.786Z00:00",strings.Replace(gjson.Get(json.Raw,"lastTransition").Str,"+","000000Z",1))
	//fmt.Println(t.Format(time.RFC3339Nano))
	re := regexpcache.MustCompile("^topology/pod-(?P<podid>[1-9][0-9]*)/node-(?P<nodeid>[1-9][0-9]*)/.*")
	match := re.FindStringSubmatch(dn)
	labels := make(map[string]string)
	if len(match) != 0 {
		for i, name := range re.SubexpNames() {
			if i != 0 && name != "" {
				labels[name] = match[i]
			}
		}
	}
	modjson := json.Raw
	if len(labels) > 0 {
		for k, v := range labels {
			modjson, _ = sjson.Set(modjson, k, v)
		}
	}
	modjson, _ = sjson.Set(modjson, "message", descr+" [dn:"+dn+"]")
	modjson, _ = sjson.Set(modjson, "fabric", fabricName)
	modjson, _ = sjson.Set(modjson, "timestamp", strings.Split(gjson.Get(json.Raw, "lastTransition").Str, "+")[0]+"000000Z")
	modjson, _ = sjson.Set(modjson, "subscribtion", subscribtionName)

	fmt.Println(modjson)
}

func (c AciConnection) getSubscribersName(mesg []byte) string {
	ids := gjson.Get(string(mesg), "subscriptionId").Array()

	if c.activeSubscribtionIds != nil {
		for _, v := range ids {

			for k, name := range c.activeSubscribtionIds {
				if v.Str == k {
					return name
				}
			}
		}
	}

	return ""
}
func (c AciConnection) getFabricName() (string, error) {
	data, err := c.getByQuery("fabric_name")
	if err != nil {
		return "", err
	}

	return gjson.Get(data, "imdata.0.infraCont.attributes.fbDmNm").Str, nil
}
func (c AciConnection) getByQuery(table string) (string, error) {
	data, err := c.get(fmt.Sprintf("%s%s", c.fabricConfig.Apic[*c.activeController], c.URLMap[table]))
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func (c AciConnection) getByClassQuery(class string, query string) (string, error) {
	data, err := c.get(fmt.Sprintf("%s/api/class/%s.json%s", c.fabricConfig.Apic[*c.activeController], class, query))
	if err != nil {
		log.WithFields(log.Fields{
			"requestid": c.ctx.Value("requestid"),
		}).Error(fmt.Sprintf("Class request %s failed - %s.", class, err))
		return "", err
	}
	return string(data), nil
}

func (c AciConnection) get(url string) ([]byte, error) {
	start := time.Now()
	body, status, err := c.doGet(url)

	log.WithFields(log.Fields{
		"method":    "GET",
		"uri":       url,
		"status":    status,
		"length":    len(body),
		"requestid": c.ctx.Value("requestid"),
		"exec_time": time.Since(start).Microseconds(),
		"system":    "monitor",
	}).Info("api call monitor system")
	return body, err
}

func (c AciConnection) doGet(url string) ([]byte, int, error) {

	req, err := http.NewRequest("GET", url, bytes.NewBuffer([]byte{}))
	if err != nil {
		log.WithFields(log.Fields{
			"requestid": c.ctx.Value("requestid"),
		}).Error(err)
		return nil, 0, err
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}

	resp, err := c.Client.GetClient().Do(req)
	if err != nil {
		log.Error(err)
		return nil, 0, err
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.WithFields(log.Fields{
				"requestid": c.ctx.Value("requestid"),
			}).Error(err)
			return nil, resp.StatusCode, err
		}

		return bodyBytes, resp.StatusCode, nil
	}
	return nil, resp.StatusCode, fmt.Errorf("ACI api returned %d", resp.StatusCode)
}

func (c AciConnection) doPostXML(url string, requestBody []byte) ([]byte, int, error, []*http.Cookie) {

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
	if err != nil {
		log.WithFields(log.Fields{
			"requestid": c.ctx.Value("requestid"),
		}).Error(err)
		return nil, 0, err, nil
	}

	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/xml")

	start := time.Now()
	resp, err := c.Client.GetClient().Do(req)
	if err != nil {
		log.WithFields(log.Fields{
			"requestid": c.ctx.Value("requestid"),
		}).Error(err)
		return nil, 0, err, nil
	}
	var status = resp.StatusCode
	log.WithFields(log.Fields{
		"method":    "POST",
		"uri":       url,
		"status":    status,
		"requestid": c.ctx.Value("requestid"),
		"exec_time": time.Since(start).Microseconds(),
		"system":    "monitor",
	}).Info("api call monitor system")

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		bodyBytes, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.WithFields(log.Fields{
				"requestid": c.ctx.Value("requestid"),
			}).Error(err)
			return nil, resp.StatusCode, err, nil
		}

		return bodyBytes, resp.StatusCode, nil, c.Client.GetJar().Cookies(req.URL)
	}

	return nil, resp.StatusCode, fmt.Errorf("ACI api returned %d", resp.StatusCode), nil
}

