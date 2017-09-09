/*
Copyright 2017 The Nuclio Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resource

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nuclio/nuclio-sdk"
	"github.com/nuclio/nuclio/pkg/zap"

	"github.com/go-chi/chi"
	"github.com/nuclio/nuclio/cmd/processor/app"
	"github.com/stretchr/testify/suite"
)

//
// Foo resource
//

type fooResource struct {
	*abstractResource
}

func (fr *fooResource) getSingle(request *http.Request) (string, attributes) {
	return "fooID", attributes{
		"a1": "v1",
		"a2": 2,
	}
}

func (fr *fooResource) getByID(request *http.Request, id string) attributes {
	if id == "dont_find_me" {
		return nil
	}

	return attributes{
		"got_id": id,
	}
}

func (fr *fooResource) getCustomRoutes() map[string]customRoute {
	return map[string]customRoute{
		"/{id}/single": {http.MethodGet, fr.getCustomSingle},
		"/{id}/multi":  {http.MethodGet, fr.getCustomMulti},
		"/post":        {http.MethodPost, fr.postCustom},
	}
}

func (fr *fooResource) getCustomSingle(request *http.Request) (string, map[string]attributes, bool, error) {
	resourceID := chi.URLParam(request, "id")

	return "getCustomSingle", map[string]attributes{
		resourceID: {"a": "b", "c": "d"},
	}, true, nil
}

func (fr *fooResource) getCustomMulti(request *http.Request) (string, map[string]attributes, bool, error) {
	resourceID := chi.URLParam(request, "id")

	return "getCustomMulti", map[string]attributes{
		resourceID:        {"a": "b", "c": "d"},
		resourceID + "-1": {"e": "f"},
	}, false, nil
}

func (fr *fooResource) postCustom(request *http.Request) (string, map[string]attributes, bool, error) {
	return "postCustom", nil, true, nil
}

//
// Test suite
//

type ResourceTestSuite struct {
	suite.Suite
	logger         nuclio.Logger
	fooResource    *fooResource
	router         chi.Router
	processor      *app.Processor
	testHTTPServer *httptest.Server
}

func (suite *ResourceTestSuite) SetupTest() {
	suite.logger, _ = nucliozap.NewNuclioZapTest("test")

	// root router
	suite.router = chi.NewRouter()

	// create a processor (unused)
	suite.processor, _ = app.NewProcessor("")

	// create the foo resource
	suite.fooResource = &fooResource{
		abstractResource: newAbstractInterface("foo", []resourceMethod{
			resourceMethodGetList,
			resourceMethodGetDetail,
		}),
	}
	suite.fooResource.resource = suite.fooResource

	suite.registerResource("foo", suite.fooResource.abstractResource)

	// set the router as the handler for requests
	suite.testHTTPServer = httptest.NewServer(suite.router)
}

func (suite *ResourceTestSuite) TearDownTest() {
	suite.testHTTPServer.Close()
}

func (suite *ResourceTestSuite) TestResourceProcessor() {
	suite.Require().Equal(suite.processor, suite.fooResource.processor)
}

func (suite *ResourceTestSuite) TestFooResourceGetList() {
	suite.sendRequest("GET", "/foo", nil, nil, `{
		"data": {
			"id": "fooID",
			"type": "foo",
			"attributes": {
				"a1": "v1",
				"a2": 2
			}
		}
	}`)
}

func (suite *ResourceTestSuite) TestFooResourceGetDetail() {
	suite.sendRequest("GET", "/foo/300", nil, nil, `{
		"data": {
			"id": "300",
			"type": "foo",
			"attributes": {
				"got_id": "300"
			}
		}
	}`)
}

func (suite *ResourceTestSuite) TestFooResourceGetDetailNotFound() {
	code := http.StatusNotFound
	suite.sendRequest("GET", "/foo/dont_find_me", nil, &code, ``)
}

func (suite *ResourceTestSuite) TestFooResourceGetCustomSingle() {
	suite.sendRequest("GET", "/foo/abc/single", nil, nil, `{
		"data": {
			"id": "abc",
			"type": "getCustomSingle",
			"attributes": {
				"a": "b",
				"c": "d"
			}
		}
	}`)
}

func (suite *ResourceTestSuite) TestFooResourceGetCustomMulti() {
	suite.sendRequest("GET", "/foo/abc/multi", nil, nil, `{
		"data": [
			{
				"id": "abc",
				"type": "getCustomMulti",
				"attributes": {
					"a": "b",
					"c": "d"
				}
			},
			{
				"id": "abc-1",
				"type": "getCustomMulti",
				"attributes": {
					"e": "f"
				}
			}
		]
	}`)
}

func (suite *ResourceTestSuite) TestFooResourcePostCustom() {
	suite.sendRequest("POST", "/foo/post", nil, nil, `{}`)
}

func (suite *ResourceTestSuite) registerResource(name string, resource *abstractResource) {

	// initialize the resource
	resource.Initialize(suite.logger, suite.processor)

	// mount it
	suite.router.Mount("/"+name, resource.router)
}

func (suite *ResourceTestSuite) sendRequest(method string,
	path string,
	requestBody io.Reader,
	expectedStatusCode *int,
	encodedExpectedResponseBody string) (*http.Response, map[string]interface{}) {

	request, err := http.NewRequest(method, suite.testHTTPServer.URL+path, nil)
	suite.Require().NoError(err)

	response, err := http.DefaultClient.Do(request)
	suite.Require().NoError(err)

	encodedResponseBody, err := ioutil.ReadAll(response.Body)
	suite.Require().NoError(err)

	defer response.Body.Close()

	suite.logger.DebugWith("Got response", "response", string(encodedResponseBody))

	// check if status code was passed
	if expectedStatusCode != nil {
		suite.Require().Equal(*expectedStatusCode, response.StatusCode)
	}

	// if there's an expected status code, verify it
	decodedResponseBody := map[string]interface{}{}

	// if we need to compare bodies
	if encodedExpectedResponseBody != "" {

		err = json.Unmarshal(encodedResponseBody, &decodedResponseBody)
		suite.Require().NoError(err)

		suite.logger.DebugWith("Comparing expected",
			"expected", suite.cleanJSONstring(encodedExpectedResponseBody))

		decodedExpectedResponseBody := map[string]interface{}{}

		err = json.Unmarshal([]byte(encodedExpectedResponseBody), &decodedExpectedResponseBody)
		suite.Require().NoError(err)

		suite.Require().True(reflect.DeepEqual(decodedExpectedResponseBody, decodedResponseBody))
	}

	return response, decodedResponseBody
}

// remove tabs and newlines
func (suite *ResourceTestSuite) cleanJSONstring(input string) string {
	for _, char := range []string{"\n", "\t"} {
		input = strings.Replace(input, char, "", -1)
	}

	return input
}

func TestResourceTestSuite(t *testing.T) {
	suite.Run(t, new(ResourceTestSuite))
}