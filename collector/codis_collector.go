package collector


import (
	"encoding/json"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"io/ioutil"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	namespace = "codis"
)

type CodisInstance struct {
	URI 		string
	Instance 	string
}


type Exporter struct {
	codisfe        string
	codis 			[]CodisInstance
	namespace    string
	duration     prometheus.Gauge
	scrapeErrors prometheus.Gauge
	totalScrapes prometheus.Counter
	metrics      map[string]*prometheus.GaugeVec
	metricsMtx   sync.RWMutex
	sync.RWMutex
}


type scrapeResult struct {
	Name  string
	Addr  string
	Value float64
}

var (
	metricMap = map[string]string{

		//the statistic metrics of redis
		"state":                      "redis_replication_state",
		"blocked_clients":            "redis_blocked_clients",
		"client_biggest_input_buf":   "redis_client_biggest_input_buf",
		"client_longest_output_list": "redis_client_longest_output_list",
		"connected_client":           "redis_connected_client",
		"instantaneous_input_kbps":   "redis_instantaneous_input_kbps",
		"instantaneous_ops_per_sec":  "redis_instantaneous_ops_per_sec",
		"instantaneous_output_kbps":  "redis_instantaneous_output_kbps",
		"keys":                 "redis_keys",
		"rejected_connections": "redis_rejected_connections",
		"repl_backlog_active":  "redis_repl_backlog_active",
		"repl_backlog_size":    "redis_repl_backlog_size",
		//"role":							"redis_role"
		"evicted_keys":               "redis_evicted_keys",
		"expired_keys":               "redis_expired_keys",
		"maxmemory":                  "redis_maxmemory",
		"used_memory":                "redis_used_memory",
		"total_commands_processed":   "redis_total_commands_processed",
		"total_connections_received": "redis_total_connections_received",
		"total_net_input_bytes":      "redis_total_net_input_bytes",
		"total_net_output_bytes":     "redis_total_net_output_bytes",
		"keyspace_hits":              "redis_keyspace_hits",
		"keyspace_misses":            "redis_keyspace_misses",
		"used_cpu_sys":               "redis_used_cpu_sys",
		"used_cpu_sys_children":      "redis_used_cpu_sys_children",
		"used_cpu_user":              "redis_used_cpu_user",
		"used_cpu_user_children":     "redis_used_cpu_user_children",
		//"redis_version":				"redis_version"
	}
	proxyMetricMap = map[string]string{
		//metrics of codis proxy
		"online":                 "proxy_online",
		"ops_total":              "proxy_ops_total",
		"ops_fails":              "proxy_ops_fails",
		"ops_errors":             "proxy_ops_redis_errors",
		"ops_qps":                "proxy_ops_qps",
		"sessions_total":         "proxy_sessions_total",
		"sessions_alive":         "proxy_sessions_alive",
		"rusage_cpu":             "proxy_rusage_cpu",
		"rusage_mem":             "proxy_rusage_mem",
		"rusage_raw_num_threads": "proxy_rusage_raw_num_threads",
		"rusage_raw_vm_size":     "proxy_rusage_raw_vm_size",
		"rusage_raw_vm_rss":      "proxy_rusage_raw_vm_rss",
	}
)

func (e *Exporter) initGauges() {
	e.metrics = map[string]*prometheus.GaugeVec{}
	for _, name := range metricMap {
		helpMsg := fmt.Sprintf("the %s of codis", name)
		e.metrics[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      name,
			Help:      helpMsg,
		}, []string{"instance", "server_addr"})
	}

	for _, name := range proxyMetricMap {
		helpMsg := fmt.Sprintf("the %s of codis proxy", name)
		e.metrics[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      name,
			Help:      helpMsg,
		}, []string{"instance", "proxy_addr"})
	}

	e.metrics["redis_role"] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "redis_role",
		Help:      "The redis role of codis",
	}, []string{"instance", "server_addr", "server_role"})
}

func NewCodisCollector(uri string, namespace string) (*Exporter, error) {
	e := Exporter{
		codisfe:     uri,
		namespace: namespace,
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "exporter_last_scrape_duration_seconds",
			Help:      "The last scrape duration.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "exporter_scrapes_total",
			Help:      "Current total codis scrapes.",
		}),
		scrapeErrors: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "exporter_last_scrape_error",
			Help:      "The last scrape error status.",
		}),
	}
	go e.RefreshCodisInstance()
	return &e, nil
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range e.metrics {
		m.Describe(ch)
	}
	ch <- e.totalScrapes.Desc()
	ch <- e.duration.Desc()
	ch <- e.scrapeErrors.Desc()
}

func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	scrapes := make(chan scrapeResult)

	e.Lock()
	defer e.Unlock()
	e.initGauges()
	go e.scrape(scrapes)
	e.setMetrics(scrapes)

	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.scrapeErrors
	e.collectMetrics(ch)

}

func (e *Exporter) scrape(scrapes chan<- scrapeResult) {
	defer close(scrapes)
	now := time.Now().UnixNano()
	e.totalScrapes.Inc()
	errorCount := 0
	for _, codis := range e.codis {
		var up float64 = 1
		if err := e.scrapeCodisUri(scrapes, codis); err != nil {
			errorCount++
			up = 0
		}
		scrapes <- scrapeResult{Name: "up", Addr: codis.Instance, Value: up}
	}
	e.scrapeErrors.Set(float64(errorCount))
	e.duration.Set(float64(time.Now().UnixNano()-now) / 1000000000)
}

func (e *Exporter) scrapeCodisUri(scrapes chan<- scrapeResult, codis CodisInstance) error {
	log.Debugf("start collect codis instance monitor %s %s",codis.Instance,codis.URI)
	resp, err := http.Get(codis.URI)
	if err != nil {
		log.Errorf(" request codis[%s] addr[%s] error",codis.Instance,codis.URI)
		return err
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("read codis[%s] addr[%s] body error",codis.Instance,codis.URI)
		return err
	}
	var result map[string]interface{}
	err = json.Unmarshal(body, &result)
	if err != nil {
		log.Errorf("parse codis[%s] addr[%s] body from byte[] to json error",codis.Instance,codis.URI)
		return err
	}
	stats := result["stats"].(map[string]interface{})
	group := stats["group"].(map[string]interface{})
	serverStats := group["stats"].(map[string]interface{})
	models := group["models"].([]interface{})
	e.metricsMtx.RLock()
	for key, val := range serverStats {
		redisS := val.(map[string]interface{})
		redisStats := redisS["stats"].(map[string]interface{})
		for k, v := range redisStats {
			vTemp := fmt.Sprintf("%s", v)
			if metricName, ok := metricMap[k]; ok {
				val, err := strconv.ParseFloat(vTemp, 64)
				log.Debugf("get metrics:%s and value is :%s", metricName, val)
				if err != nil {
					return err
				}
				e.metrics[metricName].WithLabelValues(codis.Instance, key).Set(val)
			}
			switch k {
			case "role":
				e.metrics["redis_role"].WithLabelValues(codis.Instance, key, vTemp).Set(1)
				log.Debugf("get redis_role:%s", vTemp)
			}
		}
	}
	redis_replicat_map := make(map[string]string)
	for _, val := range models {
		var server string
		valMap := val.(map[string]interface{})
		for k, v := range valMap {
			if k == "servers" {
				serverArray := v.([]interface{})
				for _, arr := range serverArray {
					dic := arr.(map[string]interface{})
					for index, value := range dic {
						if index == "server" {
							server = fmt.Sprintf("%s", value)
						}
						if index == "action" {
							stateDict := value.(map[string]interface{})
							for kt, vt := range stateDict {
								if kt == "state" && server != "" {
									redis_replication_state := fmt.Sprintf("%s", vt)
									redis_replicat_map[server] = redis_replication_state
								}
							}
						}
					}
				}
			}
		}
	}
	log.Debugf("redis servers %s", redis_replicat_map)
	for server, state := range redis_replicat_map {
		if state == "synced" {
			e.metrics["redis_replication_state"].WithLabelValues(codis.Instance, server).Set(1)
		} else {
			e.metrics["redis_replication_state"].WithLabelValues(codis.Instance, server).Set(0)
		}
	}
	proxyDict := stats["proxy"].(map[string]interface{})
	tokenAddrMap := make(map[string]string)
	if _, ok := proxyDict["models"]; ok {
		vArray := proxyDict["models"].([]interface{})
		var token, proxyAddr string
		for _, arr := range vArray {
			arrDict := arr.(map[string]interface{})
			if _, ok := arrDict["proxy_addr"]; ok {
				proxyAddr = fmt.Sprintf("%s", arrDict["proxy_addr"])
			}
			if _, ok := arrDict["token"]; ok {
				token = fmt.Sprintf("%s", arrDict["token"])
			}
			tokenAddrMap[token] = proxyAddr
		}
	}
	if proxyStats, ok := proxyDict["stats"]; ok {
		vDict := proxyStats.(map[string]interface{})
		for k1, v1 := range vDict {
			if proxyAddr, ok := tokenAddrMap[k1]; ok {
				v1Dict := v1.(map[string]interface{})
				if _, ok := v1Dict["stats"]; ok {
					statsDict := v1Dict["stats"].(map[string]interface{})
					if online, ok := statsDict["online"]; ok {
						var onlineOff float64
						if online.(bool) {
							onlineOff = 1
						} else {
							onlineOff = 0
						}
						e.metrics["proxy_online"].WithLabelValues(codis.Instance, proxyAddr).Set(onlineOff)
					}
					if ops, ok := statsDict["ops"]; ok {
						opsDict := ops.(map[string]interface{})
						log.Debugf("get opsDict is %s", opsDict)
						if total, ok := opsDict["total"]; ok {
							e.metrics["proxy_ops_total"].WithLabelValues(codis.Instance, proxyAddr).Set(total.(float64))
						}
						if fails, ok := opsDict["ops"]; ok {
							e.metrics["proxy_ops_fails"].WithLabelValues(codis.Instance, proxyAddr).Set(fails.(float64))
							log.Debugf("get proxy:%s and  fails ops  is %s", proxyAddr, fails)
						}
						if qps, ok := opsDict["qps"]; ok {
							e.metrics["proxy_ops_qps"].WithLabelValues(codis.Instance, proxyAddr).Set(qps.(float64))
							log.Debugf("get proxy:%s and  qps ops  is %s", proxyAddr, qps)
						}
					}
					if sessions, ok := statsDict["sessions"]; ok {
						sessionsDict := sessions.(map[string]interface{})
						if total, ok := sessionsDict["total"]; ok {
							e.metrics["proxy_sessions_total"].WithLabelValues(codis.Instance, proxyAddr).Set(total.(float64))
						}
						if alive, ok := sessionsDict["alive"]; ok {
							if _, ok := alive.(float64); ok {
								e.metrics["proxy_sessions_alive"].WithLabelValues(codis.Instance, proxyAddr).Set(alive.(float64))
							} else {
								e.metrics["proxy_sessions_alive"].WithLabelValues(codis.Instance, proxyAddr).Set(0)
							}
						}
					}
					if rusageDict, ok := statsDict["rusage"]; ok {
						rusageMap := rusageDict.(map[string]interface{})
						if cpu, ok := rusageMap["cpu"]; ok {
							if _, ok := cpu.(float64); ok {
								e.metrics["proxy_rusage_cpu"].WithLabelValues(codis.Instance, proxyAddr).Set(cpu.(float64))
							} else {
								e.metrics["proxy_rusage_cpu"].WithLabelValues(codis.Instance, proxyAddr).Set(0)
							}
						}
						if mem, ok := rusageMap["mem"]; ok {
							if _, ok := mem.(float64); ok {
								e.metrics["proxy_rusage_mem"].WithLabelValues(codis.Instance, proxyAddr).Set(mem.(float64))
							} else {
								e.metrics["proxy_rusage_mem"].WithLabelValues(codis.Instance, proxyAddr).Set(0)
							}
						}
						if raw, ok := rusageMap["raw"]; ok {
							rawMap := raw.(map[string]interface{})
							if numThreads, ok := rawMap["num_threads"]; ok {
								if _, ok := numThreads.(float64); ok {
									e.metrics["proxy_rusage_raw_num_threads"].WithLabelValues(codis.Instance, proxyAddr).Set(numThreads.(float64))
								} else {
									e.metrics["proxy_rusage_raw_num_threads"].WithLabelValues(codis.Instance, proxyAddr).Set(0)
								}
							}
							if vmSize, ok := rawMap["vm_size"]; ok {
								if _, ok := vmSize.(float64); ok {
									e.metrics["proxy_rusage_raw_vm_size"].WithLabelValues(codis.Instance, proxyAddr).Set(vmSize.(float64))
								} else {
									e.metrics["proxy_rusage_raw_vm_size"].WithLabelValues(codis.Instance, proxyAddr).Set(0)
								}
							}
							if vmRss, ok := rawMap["vm_rss"]; ok {
								if _, ok := vmRss.(float64); ok {
									e.metrics["proxy_rusage_raw_vm_rss"].WithLabelValues(codis.Instance, proxyAddr).Set(vmRss.(float64))
								} else {
									e.metrics["proxy_rusage_raw_vm_rss"].WithLabelValues(codis.Instance, proxyAddr).Set(0)
								}
							}
						}
					}
				}
			}
		}
	}
	defer e.metricsMtx.RUnlock()
	return nil
}

func (e *Exporter) setMetrics(scrapes <-chan scrapeResult) {
	for src := range scrapes {
		name := src.Name
		if _, ok := e.metrics[name]; !ok {
			e.metricsMtx.Lock()
			e.metrics[name] = prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: e.namespace,
				Name:      name,
			}, []string{"addr"})
			e.metricsMtx.Unlock()
		}
		var labels prometheus.Labels = map[string]string{"addr": src.Addr}
		e.metrics[name].With(labels).Set(float64(src.Value))
	}
}

func (e *Exporter) collectMetrics(metrics chan<- prometheus.Metric) {
	for _, m := range e.metrics {
		m.Collect(metrics)
	}
}

func (e *Exporter) RefreshCodisInstance() {
	interval := time.NewTicker(time.Duration(1) * time.Minute)
		for {
			log.Info("cron start get codis instance")
			err := e.getCodisInstance()
			if err != nil {
				log.Errorf("Cron GetCodis instance faild %v", err.Error())
			}
			<-interval.C
		}

}
func (e *Exporter) getCodisInstance() error {
	log.Infof("Collecting codis instance info from codis fe %s", e.codisfe)
	api := e.codisfe + "/list"			//instance list api
	resp, err := http.Get(api)
	if err != nil {
		log.Errorf("request codis fe error: %s",err)
		return err
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("read codis fe body error: %s",err)
		return err
	}
	var result []string
	err = json.Unmarshal(body, &result)
	if err != nil {
		log.Errorf("parse codis fe body from byte[] to json error: %s",err)
		return err
	}
	codis := []CodisInstance{}
	for _,instance := range result {
		api := fmt.Sprintf("%s/topom?forward=%s",e.codisfe,instance)   //get instance config api
		log.Debugf("start collect instance config %s %s" ,instance,api)
		resp, err := http.Get(api)
		if err != nil {
			log.Errorf("request codis instance[%s] error: %s",instance,err)
			continue
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Errorf("read codis instance[%s] body error: %s",instance,err)
			continue
		}
		//log.Infof("instance %s, config:%s",instance,body)
		var result map[string]interface{}
		err = json.Unmarshal(body, &result)
		if err != nil {
			log.Errorf("parse codis instance[%s] body from byte[] to json error: %s",instance,err)
			continue
		}
		model := result["model"].(map[string]interface{})
		uri := "http://" + model["admin_addr"].(string)
		instance := model["product_name"].(string)
		codis = append(codis,CodisInstance{uri,instance})
	}
	e.codis = codis
	return nil
}

