package servicemanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"runtime/trace"
	"strconv"
	"strings"
	"time"

	"github.com/couchbase/cbauth"
	"github.com/couchbase/eventing/audit"
	"github.com/couchbase/eventing/common"
	"github.com/couchbase/eventing/consumer"
	"github.com/couchbase/eventing/gen/auditevent"
	"github.com/couchbase/eventing/gen/flatbuf/cfg"
	"github.com/couchbase/eventing/logging"
	"github.com/couchbase/eventing/util"
	c "github.com/couchbase/indexing/secondary/common"
	flatbuffers "github.com/google/flatbuffers/go"
)

func (m *ServiceMgr) startTracing(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	logging.Infof("Got request to start tracing")
	audit.Log(auditevent.StartTracing, r, nil)

	os.Remove(m.uuid + "_trace.out")

	f, err := os.Create(m.uuid + "_trace.out")
	if err != nil {
		logging.Infof("Failed to open file to write trace output, err: %v", err)
		return
	}
	defer f.Close()

	err = trace.Start(f)
	if err != nil {
		logging.Infof("Failed to start runtime.Trace, err: %v", err)
		return
	}

	<-m.stopTracerCh
	trace.Stop()
}

func (m *ServiceMgr) stopTracing(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	audit.Log(auditevent.StopTracing, r, nil)
	logging.Infof("Got request to stop tracing")
	m.stopTracerCh <- struct{}{}
}

func (m *ServiceMgr) getNodeUUID(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}
	logging.Debugf("Got request to fetch UUID from host %v", r.Host)
	fmt.Fprintf(w, "%v", m.uuid)
}

func (m *ServiceMgr) debugging(w http.ResponseWriter, r *http.Request) {
	logging.Debugf("Got debugging fetch %v", r.URL)
	jsFile := path.Base(r.URL.Path)
	if strings.HasSuffix(jsFile, srcCodeExt) {
		appName := jsFile[:len(jsFile)-len(srcCodeExt)]
		handler := m.getHandler(appName)

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", handler)
		if handler == "" {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
			fmt.Fprintf(w, "App: %s not deployed", appName)
		}
	} else if strings.HasSuffix(jsFile, srcMapExt) {
		appName := jsFile[:len(jsFile)-len(srcMapExt)]
		sourceMap := m.getSourceMap(appName)

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", sourceMap)

		if sourceMap == "" {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
			fmt.Fprintf(w, "App: %s not deployed", appName)
		}
	} else {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errInvalidExt.Code))
		fmt.Fprintf(w, "Invalid extension for %s", jsFile)
	}
}

func (m *ServiceMgr) deletePrimaryStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	logging.Infof("Deleting application %v from primary store", appName)
	audit.Log(auditevent.DeleteFunction, r, appName)
	m.deletePrimaryStore(appName)
}

// Deletes application from primary store and returns the appropriate success/error code
func (m *ServiceMgr) deletePrimaryStore(appName string) (info *runtimeInfo) {
	info = &runtimeInfo{}
	logging.Infof("Deleting application %v from primary store", appName)

	checkIfDeployed := false
	for _, app := range util.ListChildren(metakvAppsPath) {
		if app == appName {
			checkIfDeployed = true
		}
	}

	if !checkIfDeployed {
		info.Code = m.statusCodes.errAppNotDeployed.Code
		info.Info = fmt.Sprintf("App: %v not deployed", appName)
		return
	}

	appState := m.superSup.GetAppState(appName)
	if appState != common.AppStateUndeployed {
		info.Code = m.statusCodes.errAppNotUndeployed.Code
		info.Info = fmt.Sprintf("Skipping delete request from primary store for app: %v as it hasn't been undeployed", appName)
		return
	}

	appList := util.ListChildren(metakvAppsPath)
	for _, app := range appList {
		if app == appName {
			settingsPath := metakvAppSettingsPath + appName
			err := util.MetaKvDelete(settingsPath, nil)
			if err != nil {
				info.Code = m.statusCodes.errDelAppSettingsPs.Code
				info.Info = fmt.Sprintf("Failed to delete setting for app: %v, err: %v", appName, err)
				return
			}

			appsPath := metakvAppsPath + appName
			err = util.MetaKvDelete(appsPath, nil)
			if err != nil {
				info.Code = m.statusCodes.errDelAppPs.Code
				info.Info = fmt.Sprintf("Failed to delete app definition for app: %v, err: %v", appName, err)
				return
			}

			info.Code = m.statusCodes.ok.Code
			info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
			return
		}
	}

	// TODO : This must be changed to app not deployed / found
	info.Code = m.statusCodes.ok.Code
	info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
	return
}

func (m *ServiceMgr) deleteTempStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	audit.Log(auditevent.DeleteDrafts, r, appName)

	m.deleteTempStore(appName)
}

// Deletes application from temporary store and returns the appropriate success/error code
func (m *ServiceMgr) deleteTempStore(appName string) (info *runtimeInfo) {
	info = &runtimeInfo{}
	logging.Infof("Deleting drafts from temporary store: %v", appName)

	checkIfDeployed := false
	for _, app := range util.ListChildren(metakvTempAppsPath) {
		if app == appName {
			checkIfDeployed = true
		}
	}

	if !checkIfDeployed {
		info.Code = m.statusCodes.errAppNotDeployed.Code
		info.Info = fmt.Sprintf("App: %v not deployed", appName)
		return
	}

	appState := m.superSup.GetAppState(appName)
	if appState != common.AppStateUndeployed {
		info.Code = m.statusCodes.errAppNotUndeployed.Code
		info.Info = fmt.Sprintf("Skipping delete request from temp store for app: %v as it hasn't been undeployed", appName)
		return
	}

	tempAppList := util.ListChildren(metakvTempAppsPath)

	for _, tempAppName := range tempAppList {
		if appName == tempAppName {
			path := metakvTempAppsPath + tempAppName
			err := util.MetaKvDelete(path, nil)
			if err != nil {
				info.Code = m.statusCodes.errDelAppTs.Code
				info.Info = fmt.Sprintf("Failed to delete from temp store for %v, err: %v", appName, err)
				return
			}

			info.Code = m.statusCodes.ok.Code
			info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
			return
		}
	}

	info.Code = m.statusCodes.ok.Code
	info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
	return
}

func (m *ServiceMgr) getDebuggerURL(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	logging.Infof("App: %v got request to get V8 debugger url", appName)

	if m.checkIfDeployed(appName) {
		debugURL := m.superSup.GetDebuggerURL(appName)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", debugURL)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getLocalDebugURL(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	appName := values["name"][0]

	logging.Infof("App: %v got request to get local V8 debugger url", appName)

	cfg := m.config.Load()
	dir := cfg["eventing_dir"].(string)

	filePath := fmt.Sprintf("%s/%s_frontend.url", dir, appName)
	u, err := ioutil.ReadFile(filePath)
	if err != nil {
		logging.Errorf("App: %v Failed to read contents from debugger frontend url file, err: %v", appName, err)
		fmt.Fprintf(w, "")
		return
	}

	fmt.Fprintf(w, "%v", string(u))
}

func (m *ServiceMgr) startDebugger(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	logging.Infof("App: %v got request to start V8 debugger", appName)
	audit.Log(auditevent.StartDebug, r, appName)

	if m.checkIfDeployed(appName) {
		m.superSup.SignalStartDebugger(appName)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "App: %v Started Debugger", appName)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) stopDebugger(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	logging.Infof("App: %v got request to stop V8 debugger", appName)
	audit.Log(auditevent.StopDebug, r, appName)

	if m.checkIfDeployed(appName) {
		m.superSup.SignalStopDebugger(appName)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "App: %v Stopped Debugger", appName)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getTimerHostPortAddrs(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	data, err := m.superSup.AppTimerTransferHostPortAddrs(appName)
	if err == nil {
		buf, err := json.Marshal(data)
		if err != nil {
			fmt.Fprintf(w, "err: %v", err)
			return
		}
		fmt.Fprintf(w, "%v", string(buf))
	}

	fmt.Fprintf(w, "")
}

func (m *ServiceMgr) getAggEventsPSec(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	logging.Debugf("Reading aggregate events processed per second for %v", appName)

	util.Retry(util.NewFixedBackoff(time.Second), getEventingNodesAddressesOpCallback, m)

	pStats, err := util.GetAggProcessedPSec(fmt.Sprintf("/getEventsPSec?name=%s", appName), m.eventingNodeAddrs)
	if err != nil {
		logging.Errorf("Failed to processing stats for app: %v, err: %v", appName, err)
		return
	}

	fmt.Fprintf(w, "%v", pStats)
}

func (m *ServiceMgr) getEventProcessingStats(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	if m.checkIfDeployed(appName) {
		stats := m.superSup.GetEventProcessingStats(appName)

		data, err := json.Marshal(&stats)
		if err != nil {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
			fmt.Fprintf(w, "Failed to marshal response event processing stats, err: %v", err)
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", string(data))

	} else {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
		fmt.Fprintf(w, "App: %v not deployed", appName)
	}
}

func (m *ServiceMgr) getEventsProcessedPSec(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	if m.checkIfDeployed(appName) {
		producerHostPortAddr := m.superSup.AppProducerHostPortAddr(appName)

		pSec, err := util.GetProcessedPSec("/getEventsPSec", producerHostPortAddr)
		if err != nil {
			logging.Errorf("Failed to capture events processed/sec stat from producer for app: %v on current node, err: %v",
				appName, err)
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%v", pSec)
	} else {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
		fmt.Fprintf(w, "App: %v not deployed", appName)
	}
}

func (m *ServiceMgr) getAggTimerHostPortAddrs(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	util.Retry(util.NewFixedBackoff(time.Second), getEventingNodesAddressesOpCallback, m)

	addrs, err := util.GetTimerHostPortAddrs(fmt.Sprintf("/getTimerHostPortAddrs?name=%s", appName), m.eventingNodeAddrs)
	if err != nil {
		logging.Errorf("Failed to marshal timer hosts for app: %v, err: %v", appName, err)
		return
	}

	fmt.Fprintf(w, "%v", addrs)
}

var getDeployedAppsCallback = func(args ...interface{}) error {
	m := args[0].(*ServiceMgr)
	aggDeployedApps := args[1].(*map[string]map[string]string)

	var err error
	*aggDeployedApps, err = util.GetDeployedApps("/getLocallyDeployedApps", m.eventingNodeAddrs)
	if err != nil {
		logging.Errorf("Failed to get deployed apps, err: %v", err)
		return err
	}

	logging.Infof("Cluster wide deployed app status: %v", aggDeployedApps)

	return nil
}

// Returns list of apps that are deployed i.e. finished dcp/timer/debugger related bootstrap
func (m *ServiceMgr) getDeployedApps(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	audit.Log(auditevent.ListDeployed, r, nil)

	util.Retry(util.NewFixedBackoff(time.Second), getEventingNodesAddressesOpCallback, m)

	aggDeployedApps := make(map[string]map[string]string)
	util.Retry(util.NewFixedBackoff(time.Second), getDeployedAppsCallback, m, &aggDeployedApps)

	appDeployedNodesCounter := make(map[string]int)

	for _, apps := range aggDeployedApps {
		for app := range apps {
			if _, ok := appDeployedNodesCounter[app]; !ok {
				appDeployedNodesCounter[app] = 0
			}

			appDeployedNodesCounter[app]++
		}
	}
	logging.Infof("DEPLOYED %v", aggDeployedApps)
	numEventingNodes := len(m.eventingNodeAddrs)
	if numEventingNodes <= 0 {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errNoEventingNodes.Code))
		fmt.Fprintf(w, "")
		return
	}

	deployedApps := make(map[string]string)
	for app, numNodesDeployed := range appDeployedNodesCounter {
		if numNodesDeployed == numEventingNodes {
			deployedApps[app] = ""
		}
	}

	buf, err := json.Marshal(deployedApps)
	if err != nil {
		logging.Errorf("Failed to marshal list of deployed apps, err: %v", err)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
		fmt.Fprintf(w, "")
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s", string(buf))
}

func (m *ServiceMgr) getLocallyDeployedApps(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	deployedApps := m.superSup.GetDeployedApps()

	buf, err := json.Marshal(deployedApps)
	if err != nil {
		logging.Errorf("Failed to marshal list of deployed apps, err: %v", err)
		fmt.Fprintf(w, "")
		return
	}

	fmt.Fprintf(w, "%s", string(buf))
}

func (m *ServiceMgr) getNamedParamsHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateLocalAuth(w, r) {
		return
	}

	w.Header().Set("Content-Type", "application/x-www-form-urlencoded")

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		return
	}

	query := string(data)
	info := util.GetNamedParams(query)
	response := url.Values{}

	response.Add("is_valid", toStr(info.PInfo.IsValid))
	response.Add("info", info.PInfo.Info)
	response.Add("named_params_size", strconv.Itoa(len(info.NamedParams)))

	for i, namedParam := range info.NamedParams {
		response.Add(strconv.Itoa(i), namedParam)
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s", response.Encode())
}

// Reports progress across all producers on current node
func (m *ServiceMgr) getRebalanceProgress(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	progress := &common.RebalanceProgress{}

	appList := util.ListChildren(metakvAppsPath)

	for _, appName := range appList {
		// TODO: Leverage error returned from rebalance task progress and fail the rebalance
		// if it occurs
		appProgress, err := m.superSup.RebalanceTaskProgress(appName)
		logging.Infof("Rebalance progress from node with rest port: %r progress: %v", m.restPort, appProgress)
		if err == nil {
			progress.VbsOwnedPerPlan += appProgress.VbsOwnedPerPlan
			progress.VbsRemainingToShuffle += appProgress.VbsRemainingToShuffle
		}
	}

	buf, err := json.Marshal(progress)
	if err != nil {
		logging.Errorf("Failed to unmarshal rebalance progress across all producers on current node, err: %v", err)
		return
	}

	w.Write(buf)
}

// Reports aggregated event processing stats from all producers
func (m *ServiceMgr) getAggEventProcessingStats(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	util.Retry(util.NewFixedBackoff(time.Second), getEventingNodesAddressesOpCallback, m)

	pStats, err := util.GetEventProcessingStats("/getEventProcessingStats?name="+appName, m.eventingNodeAddrs)
	if err != nil {
		fmt.Fprintf(w, "Failed to get event processing stats, err: %v", err)
		return
	}

	buf, err := json.Marshal(pStats)
	if err != nil {
		logging.Errorf("Failed to unmarshal event processing stats from all producers, err: %v", err)
		return
	}

	fmt.Fprintf(w, "%s", string(buf))
}

// Reports aggregated rebalance progress from all producers
func (m *ServiceMgr) getAggRebalanceProgress(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	util.Retry(util.NewFixedBackoff(time.Second), getEventingNodesAddressesOpCallback, m)

	aggProgress, _ := util.GetProgress("/getRebalanceProgress", m.eventingNodeAddrs)

	buf, err := json.Marshal(aggProgress)
	if err != nil {
		logging.Errorf("Failed to unmarshal rebalance progress across all producers, err: %v", err)
		return
	}

	w.Write(buf)
}

func (m *ServiceMgr) getLatencyStats(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	if m.checkIfDeployed(appName) {
		lStats := m.superSup.GetLatencyStats(appName)

		data, err := json.Marshal(lStats)
		if err != nil {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
			fmt.Fprintf(w, "Failed to unmarshal latency stats, err: %v\n", err)
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", string(data))
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getExecutionStats(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	if m.checkIfDeployed(appName) {
		eStats := m.superSup.GetExecutionStats(appName)

		data, err := json.Marshal(eStats)
		if err != nil {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
			fmt.Fprintf(w, "Failed to unmarshal execution stats, err: %v\n", err)
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", string(data))
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getFailureStats(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	if m.checkIfDeployed(appName) {
		fStats := m.superSup.GetFailureStats(appName)

		data, err := json.Marshal(fStats)
		if err != nil {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
			fmt.Fprintf(w, "Failed to unmarshal failure stats, err: %v\n", err)
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", string(data))
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getSeqsProcessed(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	if m.checkIfDeployed(appName) {
		seqNoProcessed := m.superSup.GetSeqsProcessed(appName)

		data, err := json.Marshal(seqNoProcessed)
		if err != nil {
			logging.Errorf("App: %v, failed to fetch vb sequences processed so far, err: %v", appName, err)

			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errGetVbSeqs.Code))
			fmt.Fprintf(w, "")
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%s", string(data))
	} else {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
		fmt.Fprintf(w, "App: %v not deployed", appName)
	}

}

func (m *ServiceMgr) setSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	audit.Log(auditevent.SetSettings, r, appName)
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		fmt.Fprintf(w, "Failed to read request body, err: %v", err)
		return
	}

	if runtimeInfo := m.setSettings(appName, data); runtimeInfo.Code != m.statusCodes.ok.Code {
		m.sendErrorInfo(w, runtimeInfo)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
}

func (m *ServiceMgr) setSettings(appName string, data []byte) (info *runtimeInfo) {
	info = &runtimeInfo{}
	logging.Infof("Set settings for app %v", appName)

	var settings map[string]interface{}
	err := json.Unmarshal(data, &settings)
	if err != nil {
		info.Code = m.statusCodes.errMarshalResp.Code
		info.Info = fmt.Sprintf("Failed to unmarshal setting supplied, err: %v", err)
		return
	}

	// Get the app from temp store and update its settings with those provided
	app, info := m.getTempStore(appName)
	if info.Code != m.statusCodes.ok.Code {
		return
	}

	for setting := range settings {
		app.Settings[setting] = settings[setting]
	}

	// State validation - app must be in deployed state
	processingStatus, pOk := app.Settings["processing_status"].(bool)
	deploymentStatus, dOk := app.Settings["deployment_status"].(bool)

	deployedApps := m.superSup.GetDeployedApps()
	if pOk && dOk {
		// Check for disable processing
		if deploymentStatus == true && processingStatus == false {
			if _, ok := deployedApps[appName]; !ok {
				info.Code = m.statusCodes.errAppNotInit.Code
				info.Info = fmt.Sprintf("App: %v not bootstrapped, discarding request to disable processing for it", appName)
				return
			}
		}

		// Check for undeploy
		if deploymentStatus == false && processingStatus == false {
			if _, ok := deployedApps[appName]; !ok {
				info.Code = m.statusCodes.errAppNotInit.Code
				info.Info = fmt.Sprintf("App: %v not bootstrapped, discarding request to undeploy it", appName)
				return
			}
		}
	} else {
		info.Code = m.statusCodes.errStatusesNotFound.Code
		info.Info = fmt.Sprintf("App: %v Missing processing or deployment statuses or both in supplied settings", appName)
		return
	}

	data, err = json.Marshal(app.Settings)
	if err != nil {
		info.Code = m.statusCodes.errMarshalResp.Code
		info.Info = fmt.Sprintf("Failed to marshal settings as JSON, err : %v", err)
		return
	}

	path := metakvAppSettingsPath + appName
	err = util.MetakvSet(path, data, nil)
	if err != nil {
		info.Code = m.statusCodes.errSetSettingsPs.Code
		info.Info = fmt.Sprintf("Failed to store setting for app: %v, err: %v", appName, err)
		return
	}

	// Write the updated app along with its settings back to temp store
	if info = m.saveTempStore(app); info.Code != m.statusCodes.ok.Code {
		return
	}

	info.Code = m.statusCodes.ok.Code
	info.Info = fmt.Sprintf("stored settings for app: %v", appName)
	return
}

func (m *ServiceMgr) getPrimaryStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	logging.Infof("Getting all applications in primary store")
	audit.Log(auditevent.FetchFunctions, r, nil)

	appList := util.ListChildren(metakvAppsPath)
	respData := make([]application, len(appList))

	for index, appName := range appList {

		path := metakvAppsPath + appName
		data, err := util.MetakvGet(path)
		if err == nil {

			config := cfg.GetRootAsConfig(data, 0)

			app := new(application)
			app.AppHandlers = string(config.AppCode())
			app.Name = string(config.AppName())
			app.ID = int(config.Id())

			d := new(cfg.DepCfg)
			depcfg := new(depCfg)
			dcfg := config.DepCfg(d)

			depcfg.MetadataBucket = string(dcfg.MetadataBucket())
			depcfg.SourceBucket = string(dcfg.SourceBucket())

			var buckets []bucket
			b := new(cfg.Bucket)
			for i := 0; i < dcfg.BucketsLength(); i++ {

				if dcfg.Buckets(b, i) {
					newBucket := bucket{
						Alias:      string(b.Alias()),
						BucketName: string(b.BucketName()),
					}
					buckets = append(buckets, newBucket)
				}
			}

			settingsPath := metakvAppSettingsPath + appName
			sData, sErr := util.MetakvGet(settingsPath)
			if sErr == nil {
				settings := make(map[string]interface{})
				uErr := json.Unmarshal(sData, &settings)
				if uErr != nil {
					logging.Errorf("Failed to unmarshal settings data from metakv, err: %v", uErr)
				} else {
					app.Settings = settings
				}
			} else {
				logging.Errorf("Failed to fetch settings data from metakv, err: %v", sErr)
			}

			depcfg.Buckets = buckets
			app.DeploymentConfig = *depcfg

			respData[index] = *app
		}
	}

	data, err := json.Marshal(respData)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
		fmt.Fprintf(w, "Failed to marshal response for get_application, err: %v", err)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s\n", data)
}

//FOR VIEW TAB SOme handler

//FOR EVENTING TAB
func (m *ServiceMgr) getTempStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, "{\"error\":\"Request not authorized\"}")
		return
	}

	// Moving just this case to trace log level as ns_server keeps polling
	// eventing every 5s to see if new functions have been created. So on an idle
	// cluster it will log lot of this message.
	logging.Tracef("Fetching function draft definitions")
	audit.Log(auditevent.FetchDrafts, r, nil)
	respData := m.getTempStoreAll()

	data, err := json.Marshal(respData)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "{\"error\":\"Failed to marshal response for stats, err: %v\"}", err)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s\n", data)
}

func (m *ServiceMgr) getTempStore(appName string) (app application, info *runtimeInfo) {
	info = &runtimeInfo{}
	logging.Infof("Fetching function draft definitions for %s", appName)

	for _, name := range util.ListChildren(metakvTempAppsPath) {
		path := metakvTempAppsPath + name
		data, err := util.MetakvGet(path)
		if err == nil {
			uErr := json.Unmarshal(data, &app)
			if uErr != nil {
				logging.Errorf("Failed to unmarshal data from metakv, err: %v", uErr)
				continue
			}

			if app.Name == appName {
				info.Code = m.statusCodes.ok.Code
				return
			}
		}
	}

	info.Code = m.statusCodes.errAppNotFoundTs.Code
	info.Info = fmt.Sprintf("Function %s not found", appName)
	return
}

//THIS IS FOR TEMPSTORE ALL for EVENTING
func (m *ServiceMgr) getTempStoreAll() []application {
	tempAppList := util.ListChildren(metakvTempAppsPath)
	applications := make([]application, len(tempAppList))

	for i, name := range tempAppList {
		path := metakvTempAppsPath + name
		data, err := util.MetakvGet(path)
		if err == nil {
			var app application
			uErr := json.Unmarshal(data, &app)
			if uErr != nil {
				logging.Errorf("Failed to unmarshal data from metakv, err: %v", uErr)
				continue
			}

			applications[i] = app
		}
	}

	return applications
}

func (m *ServiceMgr) saveTempStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	audit.Log(auditevent.SaveDraft, r, appName)

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		fmt.Fprintf(w, "Failed to read request body, err: %v", err)
		return
	}

	var app application
	err = json.Unmarshal(data, &app)
	if err != nil {
		errString := fmt.Sprintf("App: %s, Failed to unmarshal payload", appName)
		logging.Errorf("%s, err: %v", errString, err)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errUnmarshalPld.Code))
		fmt.Fprintf(w, "%s\n", errString)
		return
	}

	info := m.saveTempStore(app)
	m.sendErrorInfo(w, info)
}

// Saves application to temp store
func (m *ServiceMgr) saveTempStore(app application) (info *runtimeInfo) {
	info = &runtimeInfo{}
	appName := app.Name
	path := metakvTempAppsPath + appName
	nsServerEndpoint := net.JoinHostPort(util.Localhost(), m.restPort)
	logging.Infof("Saving handler to temporary store: %v", appName)

	cinfo, err := util.FetchNewClusterInfoCache(nsServerEndpoint)
	if err != nil {
		info.Code = m.statusCodes.errConnectNsServer.Code
		info.Info = fmt.Sprintf("Failed to initialise cluster info cache, err: %v", err)
		return
	}

	uuid := cinfo.GetBucketUUID(app.DeploymentConfig.SourceBucket)
	if uuid == "" {
		info.Code = m.statusCodes.errSrcBucketMissing.Code
		info.Info = fmt.Sprintf("Supplied source bucket: %v doesn't exist", app.DeploymentConfig.SourceBucket)
		return
	}

	uuid = cinfo.GetBucketUUID(app.DeploymentConfig.MetadataBucket)
	if uuid == "" {
		info.Code = m.statusCodes.errMetaBucketMissing.Code
		info.Info = fmt.Sprintf("Supplied metadata bucket: %v doesn't exist", app.DeploymentConfig.MetadataBucket)
		return
	}

	isMemcached, err := cinfo.IsMemcached(app.DeploymentConfig.SourceBucket)
	if err != nil {
		info.Code = m.statusCodes.errBucketTypeCheck.Code
		info.Info = fmt.Sprintf("Failed to check bucket type using cluster info cache, err: %v", err)
		return
	}

	if isMemcached {
		info.Code = m.statusCodes.errMemcachedBucket.Code
		info.Info = "Source bucket is memcached, should be either couchbase or ephemeral"
		return
	}

	data, err := json.Marshal(app)
	if err != nil {
		info.Code = m.statusCodes.errMarshalResp.Code
		info.Info = fmt.Sprintf("Failed to marshal data as JSON to save in temp store, err : %v", err)
		return
	}

	err = util.MetakvSet(path, data, nil)
	if err != nil {
		info.Code = m.statusCodes.errSaveAppTs.Code
		info.Info = fmt.Sprintf("Failed to store handlers for app: %v err: %v", appName, err)
		return
	}

	info.Code = m.statusCodes.ok.Code
	info.Info = fmt.Sprintf("Stored handlers for app: %v", appName)
	return
}

func (m *ServiceMgr) savePrimaryStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	audit.Log(auditevent.CreateFunction, r, appName)

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		errString := fmt.Sprintf("App: %s, failed to read content from http request body", appName)
		logging.Errorf("%s, err: %v", errString, err)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		fmt.Fprintf(w, "%s\n", errString)
		return
	}

	var app application
	err = json.Unmarshal(data, &app)
	if err != nil {
		errString := fmt.Sprintf("App: %s, Failed to unmarshal payload", appName)
		logging.Errorf("%s, err: %v", errString, err)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errUnmarshalPld.Code))
		fmt.Fprintf(w, "%s\n", errString)
		return
	}

	info := m.savePrimaryStore(app)
	m.sendErrorInfo(w, info)
}

// Saves application to metakv and returns appropriate success/error code
func (m *ServiceMgr) savePrimaryStore(app application) (info *runtimeInfo) {
	appName := app.Name
	info = &runtimeInfo{}
	logging.Infof("Saving application %v to primary store", appName)

	if m.checkIfDeployed(appName) {
		info.Code = m.statusCodes.errAppDeployed.Code
		info.Info = fmt.Sprintf("App with same name %s is already deployed, skipping save request", appName)
		return
	}

	if app.DeploymentConfig.SourceBucket == app.DeploymentConfig.MetadataBucket {
		info.Code = m.statusCodes.errSrcMbSame.Code
		info.Info = fmt.Sprintf("Source bucket same as metadata bucket. source_bucket : %s metadata_bucket : %s", app.DeploymentConfig.SourceBucket, app.DeploymentConfig.MetadataBucket)
		return
	}

	nsServerEndpoint := net.JoinHostPort(util.Localhost(), m.restPort)
	cinfo, err := util.FetchNewClusterInfoCache(nsServerEndpoint)
	if err != nil {
		info.Code = m.statusCodes.errConnectNsServer.Code
		info.Info = fmt.Sprintf("Failed to initialise cluster info cache, err: %v", err)
		return
	}

	uuid := cinfo.GetBucketUUID(app.DeploymentConfig.SourceBucket)
	if uuid == "" {
		info.Code = m.statusCodes.errSrcBucketMissing.Code
		info.Info = fmt.Sprintf("Supplied source bucket: %v doesn't exist", app.DeploymentConfig.SourceBucket)
		return
	}

	uuid = cinfo.GetBucketUUID(app.DeploymentConfig.MetadataBucket)
	if uuid == "" {
		info.Code = m.statusCodes.errMetaBucketMissing.Code
		info.Info = fmt.Sprintf("Supplied metadata bucket: %v doesn't exist", app.DeploymentConfig.MetadataBucket)
		return
	}

	isMemcached, err := cinfo.IsMemcached(app.DeploymentConfig.SourceBucket)
	if err != nil {
		info.Code = m.statusCodes.errBucketTypeCheck.Code
		info.Info = fmt.Sprintf("Failed to check bucket type using cluster info cache, err: %v", err)
		return
	}

	if isMemcached {
		info.Code = m.statusCodes.errMemcachedBucket.Code
		info.Info = "Source bucket is memcached, should be either couchbase or ephemeral"
		return
	}

	builder := flatbuffers.NewBuilder(0)

	var bNames []flatbuffers.UOffsetT

	for i := 0; i < len(app.DeploymentConfig.Buckets); i++ {
		alias := builder.CreateString(app.DeploymentConfig.Buckets[i].Alias)
		bName := builder.CreateString(app.DeploymentConfig.Buckets[i].BucketName)

		cfg.BucketStart(builder)
		cfg.BucketAddAlias(builder, alias)
		cfg.BucketAddBucketName(builder, bName)
		csBucket := cfg.BucketEnd(builder)

		bNames = append(bNames, csBucket)
	}

	cfg.DepCfgStartBucketsVector(builder, len(bNames))
	for i := 0; i < len(bNames); i++ {
		builder.PrependUOffsetT(bNames[i])
	}
	buckets := builder.EndVector(len(bNames))

	metaBucket := builder.CreateString(app.DeploymentConfig.MetadataBucket)
	sourceBucket := builder.CreateString(app.DeploymentConfig.SourceBucket)

	cfg.DepCfgStart(builder)
	cfg.DepCfgAddBuckets(builder, buckets)
	cfg.DepCfgAddMetadataBucket(builder, metaBucket)
	cfg.DepCfgAddSourceBucket(builder, sourceBucket)
	depcfg := cfg.DepCfgEnd(builder)

	appCode := builder.CreateString(app.AppHandlers)
	aName := builder.CreateString(app.Name)

	cfg.ConfigStart(builder)
	cfg.ConfigAddId(builder, uint32(app.ID))
	cfg.ConfigAddAppCode(builder, appCode)
	cfg.ConfigAddAppName(builder, aName)
	cfg.ConfigAddDepCfg(builder, depcfg)
	config := cfg.ConfigEnd(builder)

	builder.Finish(config)

	appContent := builder.FinishedBytes()

	c := &consumer.Consumer{}
	compilationInfo, err := c.SpawnCompilationWorker(app.AppHandlers, string(appContent), appName, m.adminHTTPPort)
	if err != nil || !compilationInfo.CompileSuccess {
		res, mErr := json.Marshal(&compilationInfo)
		if mErr != nil {
			info.Code = m.statusCodes.errMarshalResp.Code
			info.Info = fmt.Sprintf("App: %s Failed to marshal compilation status, err: %v", appName, mErr)
			return
		}

		info.Code = m.statusCodes.errHandlerCompile.Code
		info.Info = fmt.Sprintf("%v\n", string(res))
		return
	}

	settingsPath := metakvAppSettingsPath + appName
	settings := app.Settings

	mData, mErr := json.Marshal(&settings)
	if mErr != nil {
		info.Code = m.statusCodes.errMarshalResp.Code
		info.Info = fmt.Sprintf("App: %s Failed to marshal settings, err: %v", appName, mErr)
		return
	}

	mkvErr := util.MetakvSet(settingsPath, mData, nil)
	if mkvErr != nil {
		info.Code = m.statusCodes.errSetSettingsPs.Code
		info.Info = fmt.Sprintf("App: %s Failed to store updated settings in metakv, err: %v", appName, mkvErr)
		return
	}

	path := metakvAppsPath + appName
	err = util.MetakvSet(path, appContent, nil)
	if err != nil {
		info.Code = m.statusCodes.errSaveAppPs.Code
		info.Info = fmt.Sprintf("App: %v failed to write to metakv, err: %v", appName, err)
		return
	}

	info.Code = m.statusCodes.ok.Code
	info.Info = "Stored application config in metakv"
	return
}

func (m *ServiceMgr) getErrCodes(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	w.Write(m.statusPayload)
}

func (m *ServiceMgr) getDcpEventsRemaining(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]
	if m.checkIfDeployed(appName) {
		eventsRemaining := m.superSup.GetDcpEventsRemainingToProcess(appName)
		resp := backlogStat{DcpBacklog: eventsRemaining}
		data, _ := json.Marshal(&resp)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%v", string(data))
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getEventingConsumerPids(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	if m.checkIfDeployed(appName) {
		workerPidMapping := m.superSup.GetEventingConsumerPids(appName)

		data, err := json.Marshal(&workerPidMapping)
		if err != nil {
			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
			fmt.Fprintf(w, "Failed to marshal consumer pids, err: %v", err)
			return
		}

		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
		fmt.Fprintf(w, "%v", string(data))
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errAppNotDeployed.Code))
	fmt.Fprintf(w, "App: %v not deployed", appName)
}

func (m *ServiceMgr) getCreds(w http.ResponseWriter, r *http.Request) {
	if !m.validateLocalAuth(w, r) {
		return
	}

	w.Header().Set("Content-Type", "application/x-www-form-urlencoded")

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		fmt.Fprintf(w, "Failed to read request body, err: %v", err)
		return
	}

	var username, password string
	util.Retry(util.NewFixedBackoff(time.Second), util.GetCredsCallback, string(data), &username, &password)

	response := url.Values{}
	response.Add("username", username)
	response.Add("password", password)

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s", response.Encode())
}

func (m *ServiceMgr) validateAuth(w http.ResponseWriter, r *http.Request, perm string) bool {
	creds, err := cbauth.AuthWebCreds(r)
	if err != nil || creds == nil {
		logging.Warnf("Cannot authenticate request to %r", r.URL)
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	allowed, err := creds.IsAllowed(perm)
	if err != nil || !allowed {
		logging.Warnf("Cannot authorize request to %r", r.URL)
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	logging.Debugf("Allowing access to %r", r.URL)
	return true
}

func (m *ServiceMgr) validateLocalAuth(w http.ResponseWriter, r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		logging.Warnf("Unable to verify remote in request to %r: %r", r.URL, err)
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	pip := net.ParseIP(ip)
	if pip == nil || !pip.IsLoopback() {
		logging.Warnf("Forbidden remote in request to %r: %r", r.URL, r)
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	r_usr, r_key, ok := r.BasicAuth()
	if !ok {
		logging.Warnf("No credentials on request to %r", r.URL)
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	usr, key := util.LocalKey()
	if r_usr != usr || r_key != key {
		logging.Warnf("Cannot authorize request to %r", r.URL)
		w.WriteHeader(http.StatusForbidden)
		return false
	}
	logging.Debugf("Allowing access to %r", r.URL)
	return true
}

func (m *ServiceMgr) clearEventStats(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	logging.Infof("Got request to clear event stats from host: %r", r.Host)
	m.superSup.ClearEventStats()
}

func (m *ServiceMgr) getHandler(appName string) string {
	if m.checkIfDeployed(appName) {
		return m.superSup.GetHandlerCode(appName)
	}

	return ""
}

func (m *ServiceMgr) getSourceMap(appName string) string {
	if m.checkIfDeployed(appName) {
		return m.superSup.GetSourceMap(appName)
	}

	return ""
}

func (m *ServiceMgr) checkIfDeployed(appName string) bool {
	deployedApps := m.superSup.DeployedAppList()
	for _, app := range deployedApps {
		if app == appName {
			return true
		}
	}
	return false
}

func (m *ServiceMgr) unmarshalApp(r *http.Request) (app application, info *runtimeInfo) {
	info = &runtimeInfo{}

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		info.Code = m.statusCodes.errReadReq.Code
		info.Info = fmt.Sprintf("Failed to read request body, err: %v", err)
		logging.Errorf(info.Info)
		return
	}

	err = json.Unmarshal(data, &app)
	if err != nil {
		info.Code = m.statusCodes.errUnmarshalPld.Code
		info.Info = fmt.Sprintf("Failed to unmarshal payload err: %v", err)
		logging.Errorf(info.Info)
		return
	}

	info.Code = m.statusCodes.ok.Code
	info.Info = "OK"
	return
}

// Unmarshals list of application and returns application objects
func (m *ServiceMgr) unmarshalAppList(w http.ResponseWriter, r *http.Request) []application {
	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		fmt.Fprintf(w, "Failed to read request body, err: %v", err)
		return nil
	}

	var appList []application
	err = json.Unmarshal(data, &appList)
	if err != nil {
		errString := fmt.Sprintf("Failed to unmarshal payload err: %v", err)
		logging.Errorf("%s", errString)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errUnmarshalPld.Code))
		fmt.Fprintf(w, "%s\n", errString)
		return nil
	}

	return appList
}

func (m *ServiceMgr) parseQueryHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateLocalAuth(w, r) {
		return
	}

	w.Header().Set("Content-Type", "application/x-www-form-urlencoded")

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		return
	}

	query := string(data)
	info := util.Parse(query)
	response := url.Values{}

	response.Add("is_valid", toStr(info.IsValid))
	response.Add("info", info.Info)

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s", response.Encode())
}

func (m *ServiceMgr) getConfig() (c config, info *runtimeInfo) {
	info = &runtimeInfo{}
	data, err := util.MetakvGet(metakvConfigPath)
	if err != nil {
		info.Code = m.statusCodes.errGetConfig.Code
		info.Info = fmt.Sprintf("Failed to get config, err: %v", err)
		return
	}

	if !bytes.Equal(data, nil) {
		err = json.Unmarshal(data, &c)
		if err != nil {
			info.Code = m.statusCodes.errUnmarshalPld.Code
			info.Info = fmt.Sprintf("Failed to unmarshal payload from metakv, err: %v", err)
			return
		}
	}

	logging.Infof("Retrieving config from metakv: %r", c)
	info.Code = m.statusCodes.ok.Code
	return
}

func (m *ServiceMgr) saveConfig(c config) (info *runtimeInfo) {
	info = &runtimeInfo{}
	data, err := json.Marshal(c)
	if err != nil {
		info.Code = m.statusCodes.errMarshalResp.Code
		info.Info = fmt.Sprintf("Failed to marshal config, err: %v", err)
		return
	}

	logging.Infof("Saving config into metakv: %v", c)
	err = util.MetakvSet(metakvConfigPath, data, nil)
	if err != nil {
		info.Code = m.statusCodes.errSaveConfig.Code
		info.Info = fmt.Sprintf("Failed to store config to meta kv, err: %v", err)
		return
	}

	info.Code = m.statusCodes.ok.Code
	return
}

func (m *ServiceMgr) configHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !m.validateAuth(w, r, EventingPermissionManage) {
		fmt.Fprintln(w, "{\"error\":\"Request not authorized\"}")
		return
	}

	info := &runtimeInfo{}

	switch r.Method {
	case "GET":
		audit.Log(auditevent.FetchConfig, r, nil)

		c, info := m.getConfig()
		if info.Code != m.statusCodes.ok.Code {
			m.sendErrorInfo(w, info)
			return
		}

		response, err := json.Marshal(c)
		if err != nil {
			info.Code = m.statusCodes.errMarshalResp.Code
			info.Info = fmt.Sprintf("Failed to marshal config, err : %v", err)
			m.sendErrorInfo(w, info)
			return
		}

		fmt.Fprintf(w, "%s", string(response))

	case "POST":
		audit.Log(auditevent.SaveConfig, r, nil)

		data, err := ioutil.ReadAll(r.Body)
		if err != nil {
			info.Code = m.statusCodes.errReadReq.Code
			info.Info = fmt.Sprintf("Failed to read request body, err: %v", err)
			m.sendErrorInfo(w, info)
			return
		}

		var c config
		err = json.Unmarshal(data, &c)
		if err != nil {
			info.Code = m.statusCodes.errUnmarshalPld.Code
			info.Info = fmt.Sprintf("Failed to unmarshal config from metakv, err: %v", err)
			m.sendErrorInfo(w, info)
			return
		}

		if info = m.saveConfig(c); info.Code != m.statusCodes.ok.Code {
			m.sendErrorInfo(w, info)
		}

		response := configResponse{false}
		data, err = json.Marshal(response)
		if err != nil {
			info.Code = m.statusCodes.errMarshalResp.Code
			info.Info = fmt.Sprintf("Failed to marshal response, err: %v", err)
			m.sendErrorInfo(w, info)
			return
		}

		fmt.Fprintf(w, "%s", string(data))
	}
}

func (m *ServiceMgr) functionsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !m.validateAuth(w, r, EventingPermissionManage) {
		fmt.Fprintln(w, "{\"error\":\"Request not authorized\"}")
		return
	}

	functions := regexp.MustCompile("^/api/v1/functions/?$")
	functionsName := regexp.MustCompile("^/api/v1/functions/(.+[^/])/?$") // Match is agnostic of trailing '/'
	functionsNameSettings := regexp.MustCompile("^/api/v1/functions/(.+[^/])/settings/?$")
	info := &runtimeInfo{}

	if match := functionsNameSettings.FindStringSubmatch(r.URL.Path); len(match) != 0 {
		appName := match[1]
		switch r.Method {
		case "POST":
			audit.Log(auditevent.SetSettings, r, appName)

			data, err := ioutil.ReadAll(r.Body)
			if err != nil {
				info.Code = m.statusCodes.errReadReq.Code
				info.Info = fmt.Sprintf("Failed to read request body, err: %v", err)
				m.sendErrorInfo(w, info)
				return
			}

			if info = m.setSettings(appName, data); info.Code != m.statusCodes.ok.Code {
				m.sendErrorInfo(w, info)
			}
		}
	} else if match := functionsName.FindStringSubmatch(r.URL.Path); len(match) != 0 {
		appName := match[1]
		switch r.Method {
		case "GET":
			audit.Log(auditevent.FetchDrafts, r, appName)

			app, info := m.getTempStore(appName)
			if info.Code != m.statusCodes.ok.Code {
				w.WriteHeader(http.StatusNotFound)
				m.sendErrorInfo(w, info)
				return
			}

			response, err := json.Marshal(app)
			if err != nil {
				info.Code = m.statusCodes.errMarshalResp.Code
				info.Info = fmt.Sprintf("Failed to marshal app, err : %v", err)
				m.sendErrorInfo(w, info)
				return
			}

			w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
			fmt.Fprintf(w, "%s", string(response))

		case "POST":
			audit.Log(auditevent.CreateFunction, r, appName)

			app, info := m.unmarshalApp(r)
			if info.Code != m.statusCodes.ok.Code {
				m.sendErrorInfo(w, info)
				return
			}

			// Reject the request if there is a mismatch of app name in URL and body
			if app.Name != appName {
				info.Code = m.statusCodes.errAppNameMismatch.Code
				info.Info = fmt.Sprintf("Function name in the URL (%s) and body (%s) must be same", appName, app.Name)
				w.WriteHeader(http.StatusBadRequest)
				m.sendErrorInfo(w, info)
				return
			}

			// Save to temp store only if saving to primary store succeeds
			if runtimeInfo := m.savePrimaryStore(app); runtimeInfo.Code == m.statusCodes.ok.Code {
				audit.Log(auditevent.SaveDraft, r, appName)

				if runtimeInfo := m.saveTempStore(app); runtimeInfo.Code != m.statusCodes.ok.Code {
					m.sendErrorInfo(w, runtimeInfo)
					return
				}
			} else {
				m.sendErrorInfo(w, runtimeInfo)
			}

		case "DELETE":
			audit.Log(auditevent.DeleteFunction, r, appName)

			info := m.deletePrimaryStore(appName)
			// Delete the application from temp store only if app does not exist in primary store
			// or if the deletion succeeds on primary store
			if info.Code == m.statusCodes.errAppNotDeployed.Code || info.Code == m.statusCodes.ok.Code {
				audit.Log(auditevent.DeleteDrafts, r, appName)

				if runtimeInfo := m.deleteTempStore(appName); runtimeInfo.Code != m.statusCodes.ok.Code {
					m.sendErrorInfo(w, runtimeInfo)
					return
				}
			} else {
				m.sendErrorInfo(w, info)
			}
		}

	} else if match := functions.FindStringSubmatch(r.URL.Path); len(match) != 0 {
		switch r.Method {
		case "GET":
			m.getTempStoreHandler(w, r)

		case "POST":
			infoList := []*runtimeInfo{}
			appList := m.unmarshalAppList(w, r)
			for _, app := range appList {
				audit.Log(auditevent.CreateFunction, r, app.Name)

				// Save to temp store only if saving to primary store succeeds
				if info := m.savePrimaryStore(app); info.Code == m.statusCodes.ok.Code {
					audit.Log(auditevent.SaveDraft, r, app.Name)

					info := m.saveTempStore(app)
					infoList = append(infoList, info)
				} else {
					infoList = append(infoList, info)
				}
			}

			m.sendRuntimeInfoList(w, infoList)

		case "DELETE":
			infoList := []*runtimeInfo{}
			for _, app := range m.getTempStoreAll() {
				audit.Log(auditevent.DeleteFunction, r, app.Name)

				info := m.deletePrimaryStore(app.Name)
				// Delete the application from temp store only if app does not exist in primary store
				// or if the deletion succeeds on primary store
				if info.Code == m.statusCodes.errAppNotDeployed.Code || info.Code == m.statusCodes.ok.Code {
					audit.Log(auditevent.DeleteDrafts, r, app.Name)

					info := m.deleteTempStore(app.Name)
					infoList = append(infoList, info)
				} else {
					infoList = append(infoList, info)
				}
			}

			m.sendRuntimeInfoList(w, infoList)
		}
	}
}

func (m *ServiceMgr) libraryHandler(w http.ResponseWriter, r *http.Request) {
}

func (m *ServiceMgr) statsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !m.validateAuth(w, r, EventingPermissionManage) {
		fmt.Fprintln(w, "{\"error\":\"Request not authorized\"}")
		return
	}

	if r.Method == "GET" {
		// Check whether type=full is present in query
		fullStats := false
		if typeParam := r.URL.Query().Get("type"); typeParam != "" {
			fullStats = typeParam == "full"
		}

		statsList := make([]stats, 0)
		for _, app := range m.getTempStoreAll() {
			if m.checkIfDeployed(app.Name) {
				stats := stats{}
				stats.EventProcessingStats = m.superSup.GetEventProcessingStats(app.Name)
				stats.EventsRemaining = backlogStat{DcpBacklog: m.superSup.GetDcpEventsRemainingToProcess(app.Name)}
				stats.ExecutionStats = m.superSup.GetExecutionStats(app.Name)
				stats.FailureStats = m.superSup.GetFailureStats(app.Name)
				stats.FunctionName = app.Name
				stats.LcbExceptionStats = m.superSup.GetLcbExceptionsStats(app.Name)
				stats.WorkerPids = m.superSup.GetEventingConsumerPids(app.Name)
				stats.PlannerStats = m.superSup.PlannerStats(app.Name)
				stats.VbDistributionStats = m.superSup.VbDistributionStats(app.Name)

				if fullStats {
					stats.LatencyStats = m.superSup.GetLatencyStats(app.Name)

					plasmaStats, err := m.superSup.GetPlasmaStats(app.Name)
					if err == nil {
						stats.PlasmaStats = plasmaStats
					}

					stats.SeqsProcessed = m.superSup.GetSeqsProcessed(app.Name)
					stats.VbDcpEventsRemaining = m.superSup.VbDcpEventsRemainingToProcess(app.Name)
					debugStats, err := m.superSup.TimerDebugStats(app.Name)
					if err == nil {
						stats.DocTimerDebugStats = debugStats
					}
				}

				statsList = append(statsList, stats)
			}
		}

		response, err := json.Marshal(statsList)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "{\"error\":\"Failed to marshal response for stats, err: %v\"}", err)
			return
		}

		fmt.Fprintf(w, "%s", string(response))
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}

	return
}

// Clears up all Eventing related artifacts from metakv, typically will be used for rebalance tests
func (m *ServiceMgr) cleanupEventing(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		fmt.Fprintln(w, "{\"error\":\"Request not authorized\"}")
		return
	}

	audit.Log(auditevent.CleanupEventing, r, nil)

	util.Retry(util.NewFixedBackoff(time.Second), cleanupEventingMetaKvPath, metakvAppsPath)
	util.Retry(util.NewFixedBackoff(time.Second), cleanupEventingMetaKvPath, metakvTempAppsPath)
	util.Retry(util.NewFixedBackoff(time.Second), cleanupEventingMetaKvPath, metakvAppSettingsPath)
}

//FOR VIEWS

func (m *ServiceMgr) deleteLibraryHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	logging.Infof("Deleting application %v from primary store", appName)
	audit.Log(auditevent.DeleteFunction, r, appName)
	m.deleteViewStore(appName)
}
func (m *ServiceMgr) deleteViewStore(appName string) {
	info := &runtimeInfo{}
	var err error
	//See whether index is based on this or not if yes then Dont delete give some message

	appList := util.ListChildren(metakvViewAppsPath)
	for _, app := range appList {
		if app == appName {

			appsPath := metakvViewAppsPath + appName
			err = util.MetaKvDelete(appsPath, nil)
			if err != nil {
				info.Code = m.statusCodes.errDelAppPs.Code
				info.Info = fmt.Sprintf("Failed to delete app definition for app: %v, err: %v", appName, err)
				return
			}

			info.Code = m.statusCodes.ok.Code
			info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
			return
		}
	}

	// TODO : This must be changed to app not deployed / found
	info.Code = m.statusCodes.ok.Code
	info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
	return
}

//No need of this module
func (m *ServiceMgr) deleteTempLibraryHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	values := r.URL.Query()
	appName := values["name"][0]

	audit.Log(auditevent.DeleteDrafts, r, appName)

	m.deleteViewTempStore(appName)
}

func (m *ServiceMgr) deleteViewTempStore(appName string) (info *runtimeInfo) {
	info = &runtimeInfo{}
	logging.Infof("Deleting drafts from temporary store: %v", appName)

	checkIfDeployed := false
	for _, app := range util.ListChildren(metakvTempViewAppsPath) {
		if app == appName {
			checkIfDeployed = true
		}
	}

	if !checkIfDeployed {
		info.Code = m.statusCodes.errAppNotDeployed.Code
		info.Info = fmt.Sprintf("App: %v not deployed", appName)
		return
	}

	tempAppList := util.ListChildren(metakvTempViewAppsPath)

	for _, tempAppName := range tempAppList {
		if appName == tempAppName {
			path := metakvTempViewAppsPath + tempAppName
			err := util.MetaKvDelete(path, nil)
			if err != nil {
				info.Code = m.statusCodes.errDelAppTs.Code
				info.Info = fmt.Sprintf("Failed to delete from temp store for %v, err: %v", appName, err)
				return
			}

			info.Code = m.statusCodes.ok.Code
			info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
			return
		}
	}

	info.Code = m.statusCodes.ok.Code
	info.Info = fmt.Sprintf("Deleting app: %v in the background", appName)
	return
}

func (m *ServiceMgr) getViewStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	logging.Infof("Getting all applications in primary store")
	audit.Log(auditevent.FetchFunctions, r, nil)

	appList := util.ListChildren(metakvViewAppsPath)
	respData := make([]jsonType, len(appList))

	for index, appName := range appList {

		path := metakvViewAppsPath + appName
		data, err := util.MetakvGet(path)
		var details jsonType
		err = json.Unmarshal(data, &details)
		if err == nil {
			respData[index] = details
		}
	}

	data, err := json.Marshal(respData)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
		fmt.Fprintf(w, "Failed to marshal response for get_application, err: %v", err)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s\n", data)
}

func (m *ServiceMgr) getTempViewHandler(w http.ResponseWriter, r *http.Request) {
	logging.Infof("GET TEMPHANDLER")
	if !m.validateAuth(w, r, EventingPermissionManage) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintln(w, "{\"error\":\"Request not authorized\"}")
		return
	}
	logging.Infof("TEMP HANDLER")
	respData := m.getTempLibraryStoreAll()

	data, err := json.Marshal(respData)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errMarshalResp.Code))
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "{\"error\":\"Failed to marshal response for stats, err: %v\"}", err)
		return
	}

	w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.ok.Code))
	fmt.Fprintf(w, "%s\n", data)
}

func (m *ServiceMgr) getTempLibraryStoreAll() []jsonType {
	logging.Infof("ALL ")
	tempAppList := util.ListChildren(metakvViewAppsPath)
	logging.Infof("TEMPLIST %v", tempAppList)
	applications := make([]jsonType, len(tempAppList))

	for i, name := range tempAppList {
		path := metakvViewAppsPath + name
		data, err := util.MetakvGet(path)
		if err == nil {
			var app jsonType
			uErr := json.Unmarshal(data, &app)
			if uErr != nil {
				logging.Errorf("Failed to unmarshal data from metakv, err: %v", uErr)
				continue
			}

			applications[i] = app
		}
	}

	return applications
}

func (m *ServiceMgr) saveViewStoreHandler(w http.ResponseWriter, r *http.Request) {
	if !m.validateAuth(w, r, EventingPermissionManage) {
		return
	}

	params := r.URL.Query()
	appName := params["name"][0]

	data, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errReadReq.Code))
		fmt.Fprintf(w, "Failed to read request body, err: %v", err)
		return
	}
	logging.Infof("APPNAME :%s", appName)
	var app jsonType
	err = json.Unmarshal(data, &app)
	logging.Infof("JSOND: %v %v %v", app.Name, app.AppCode, app.Description)
	if err != nil {
		errString := fmt.Sprintf("App: %s, Failed to unmarshal payload", appName)
		logging.Errorf("%s, err: %v", errString, err)
		w.Header().Add(headerKey, strconv.Itoa(m.statusCodes.errUnmarshalPld.Code))
		fmt.Fprintf(w, "%s\n", errString)
		return
	}

	info := m.savePrimaryStoreView(app)
	m.sendErrorInfo(w, info)
}

func (m *ServiceMgr) savePrimaryStoreView(app jsonType) (info *runtimeInfo) {
	appName := app.Name
	info = &runtimeInfo{}
	path := metakvViewAppsPath + appName
	data, err := json.Marshal(app)
	if err != nil {
		logging.Infof("ERROR %v", err)
		info.Code = m.statusCodes.errMarshalResp.Code
		info.Info = fmt.Sprintf("Failed to marshal data as JSON to save in temp store, err : %v", err)
		return
	}
	err = util.MetakvSet(path, data, nil)

/*
	//GETTING CODE IN THE PROJECTOR
	type Code struct {
		Code string `json:"appcode"`
	}
	var t testing
	_, err = c.MetakvGet(path, &t)
*/

	info.Code = m.statusCodes.ok.Code
	info.Info = "Stored application config in metakv"

	return
}
