package agent

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seldonio/seldon-core/scheduler/pkg/envoy/resources"

	"github.com/gorilla/mux"

	. "github.com/onsi/gomega"
	log "github.com/sirupsen/logrus"
)

const (
	backEndServerPort = 8088
)

func v2_infer(w http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	model_name := params["model_name"]
	_, _ = w.Write([]byte("Model inference: " + model_name))
}

func v2_load(w http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	model_name := params["model_name"]
	_, _ = w.Write([]byte("Model load: " + model_name))
}

func v2_unload(w http.ResponseWriter, req *http.Request) {
	params := mux.Vars(req)
	model_name := params["model_name"]
	_, _ = w.Write([]byte("Model unload: " + model_name))
}

func isRegistered(port int) bool {
	timeout := 5 * time.Second
	conn, err := net.DialTimeout("tcp", ":"+strconv.Itoa(port), timeout)
	if err != nil {
		return false
	}

	if conn != nil {
		conn.Close()
		return true
	}

	return false
}
func setupMockMLServer() {
	if isRegistered(backEndServerPort) {
		log.Warnf("Port %d already running", backEndGRPCServerPort)
		return
	}
	rtr := mux.NewRouter()
	rtr.HandleFunc("/v2/models/{model_name:\\w+}/infer", v2_infer).Methods("POST")
	rtr.HandleFunc("/v2/repository/models/{model_name:\\w+}/load", v2_load).Methods("POST")
	rtr.HandleFunc("/v2/repository/models/{model_name:\\w+}/unload", v2_unload).Methods("POST")

	http.Handle("/", rtr)

	if err := http.ListenAndServe(":"+strconv.Itoa(backEndServerPort), nil); err != nil {
		log.Warn(err)
	}
}

func setupReverseProxy(logger log.FieldLogger, numModels int, modelPrefix string) *reverseHTTPProxy {
	v2Client := NewV2Client("localhost", backEndServerPort, logger)
	localCacheManager := setupLocalTestManager(numModels, modelPrefix, v2Client, numModels-2)
	rp := NewReverseHTTPProxy(logger, ReverseProxyHTTPPort)
	rp.SetState(localCacheManager)
	return rp
}

func TestReverseProxySmoke(t *testing.T) {
	g := NewGomegaWithT(t)
	logger := log.New()
	logger.SetLevel(log.DebugLevel)

	type test struct {
		name           string
		modelToLoad    string
		modelToRequest string
		statusCode     int
	}

	tests := []test{
		{
			name:           "model exists",
			modelToLoad:    "foo",
			modelToRequest: "foo",
			statusCode:     200,
		},
		{
			name:           "model does not exists",
			modelToLoad:    "foo",
			modelToRequest: "foo2",
			statusCode:     404,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			go setupMockMLServer()
			rpHTTP := setupReverseProxy(logger, 3, test.modelToLoad)
			err := rpHTTP.Start()
			g.Expect(err).To(BeNil())

			// load model
			rpHTTP.stateManager.modelVersions.addModelVersion(
				getDummyModelDetails(test.modelToLoad, uint64(1), uint32(1)))

			// make a dummy predict call with any model name
			inferV2Path := "/v2/models/RANDOM/infer"
			url := "http://localhost:" + strconv.Itoa(ReverseProxyHTTPPort) + inferV2Path
			req, err := http.NewRequest(http.MethodPost, url, nil)
			g.Expect(err).To(BeNil())
			req.Header.Set("contentType", "application/json")
			req.Header.Set(resources.SeldonInternalModel, test.modelToRequest)
			resp, err := http.DefaultClient.Do(req)
			g.Expect(err).To(BeNil())
			defer resp.Body.Close()

			g.Expect(resp.StatusCode).To(Equal(test.statusCode))
			if test.statusCode == 200 {
				bodyBytes, err := io.ReadAll(resp.Body)
				g.Expect(err).To(BeNil())
				bodyString := string(bodyBytes)
				g.Expect(strings.Contains(bodyString, test.modelToLoad)).To(BeTrue())
			}
			g.Expect(rpHTTP.Ready()).To(Equal(true))
			_ = rpHTTP.Stop()
			g.Expect(rpHTTP.Ready()).To(Equal(false))
		})
	}

}

func TestRewritePath(t *testing.T) {
	g := NewGomegaWithT(t)
	type test struct {
		name         string
		path         string
		modelName    string
		expectedPath string
	}
	tests := []test{
		{
			name:         "default infer",
			path:         "/v2/models/iris/infer",
			modelName:    "foo",
			expectedPath: "/v2/models/foo/infer",
		},
		{
			name:         "default infer model with dash",
			path:         "/v2/models/iris-1/infer",
			modelName:    "foo",
			expectedPath: "/v2/models/foo/infer",
		},
		{
			name:         "default infer model with underscore",
			path:         "/v2/models/iris_1/infer",
			modelName:    "foo",
			expectedPath: "/v2/models/foo/infer",
		},
		{
			name:         "metadata for model",
			path:         "/v2/models/iris",
			modelName:    "foo",
			expectedPath: "/v2/models/foo",
		},
		{
			name:         "for server calls no change",
			path:         "/v2/health/live",
			modelName:    "foo",
			expectedPath: "/v2/health/live",
		},
		{
			name:         "versioned infer",
			path:         "/v2/models/iris/versions/1/infer",
			modelName:    "foo",
			expectedPath: "/v2/models/foo/versions/1/infer",
		},
		{
			name:         "model ready",
			path:         "/v2/models/iris/ready",
			modelName:    "foo",
			expectedPath: "/v2/models/foo/ready",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rewrittenPath := rewritePath(test.path, test.modelName)
			g.Expect(rewrittenPath).To(Equal(test.expectedPath))
		})
	}
}
