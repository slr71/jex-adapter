package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/cyverse-de/configurate"
	"gopkg.in/cyverse-de/messaging.v6"
	"gopkg.in/cyverse-de/model.v4"

	"github.com/spf13/viper"
	"github.com/streadway/amqp"
)

var (
	s   *model.Job
	cfg *viper.Viper
)

func shouldrun() bool {
	if os.Getenv("RABBIT_PORT_5672_TCP_ADDR") != "" {
		return true
	}
	return false
}

func uri() string {
	addr := os.Getenv("RABBIT_PORT_5672_TCP_ADDR")
	port := os.Getenv("RABBIT_PORT_5672_TCP_PORT")
	return fmt.Sprintf("amqp://guest:guest@%s:%s/", addr, port)
}

func JSONData() ([]byte, error) {
	f, err := os.Open("test/test_submission.json")
	if err != nil {
		return nil, err
	}
	c, err := ioutil.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return c, err
}

func _inittests(t *testing.T, memoize bool) *model.Job {
	var err error
	if s == nil || !memoize {
		cfg, err = configurate.Init("test/test_config.yaml")
		if err != nil {
			t.Error(err)
		}
		cfg.Set("irods.base", "/path/to/irodsbase")
		cfg.Set("irods.host", "hostname")
		cfg.Set("irods.port", "1247")
		cfg.Set("irods.user", "user")
		cfg.Set("irods.pass", "pass")
		cfg.Set("irods.zone", "test")
		cfg.Set("irods.resc", "")
		cfg.Set("condor.log_path", "/path/to/logs")
		cfg.Set("condor.porklock_tag", "test")
		cfg.Set("condor.filter_files", "foo,bar,baz,blippy")
		cfg.Set("condor.request_disk", "0")
		data, err := JSONData()
		if err != nil {
			t.Error(err)
		}
		s, err = model.NewFromData(cfg, data)
		if err != nil {
			t.Error(err)
		}
	}
	return s
}

func inittests(t *testing.T) *model.Job {
	return _inittests(t, true)
}

func TestGetHome(t *testing.T) {
	app := New(cfg)
	req, err := http.NewRequest("GET", "http://for-a-test.org", nil)
	if err != nil {
		t.Error(err)
	}
	recorder := httptest.NewRecorder()
	app.home(recorder, req)
	actual := recorder.Body.String()
	expected := "Welcome to the JEX.\n"
	if actual != expected {
		t.Errorf("home() returned %s instead of %s", actual, expected)
	}
}

func TestStop(t *testing.T) {
	if !shouldrun() {
		return
	}
	app := New(cfg)
	invID := "test-invocation-id"
	stopKey := fmt.Sprintf("%s.%s", messaging.StopsKey, invID)
	exitChan := make(chan int)
	client, err := messaging.NewClient(uri(), false)
	exchangeName := app.cfg.GetString("amqp.exchange.name")
	if err != nil {
		t.Error(err)
	}
	defer client.Close()
	client.AddConsumer(exchangeName, "topic", "test_stop", stopKey, func(d amqp.Delivery) {
		d.Ack(false)
		stopMsg := &messaging.StopRequest{}
		err = json.Unmarshal(d.Body, stopMsg)
		if err != nil {
			t.Error(err)
		}
		actual := stopMsg.Reason
		expected := "User request"
		if actual != expected {
			t.Errorf("messaging.StopRequest.Reason was %s instead of %s", actual, expected)
		}
		actual = stopMsg.Username
		expected = "system"
		if actual != expected {
			t.Errorf("messaging.StopRequest.Username was %s instead of %s", actual, expected)
		}
		actual = stopMsg.InvocationID
		expected = invID
		if actual != expected {
			t.Errorf("messaging.StopRequest.InvocationID was %s instead of %s", actual, expected)
		}
		exitChan <- 1
	},
		0)

	client.SetupPublishing(exchangeName)
	go client.Listen()
	time.Sleep(100 * time.Millisecond)
	requestURL := fmt.Sprintf("http://for-a-test.org/stop/%s", invID)
	request, err := http.NewRequest("DELETE", requestURL, nil)
	if err != nil {
		t.Error(err)
	}
	recorder := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Errorf("stop() didn't return a 200 status code: %d", recorder.Code)
	}
	<-exitChan
}

func TestLaunch(t *testing.T) {
	if !shouldrun() {
		return
	}
	app := New(cfg)
	job := inittests(t)
	exitChan := make(chan int)
	client, err := messaging.NewClient(uri(), false)
	exchangeName := app.cfg.GetString("amqp.exchange.name")
	if err != nil {
		t.Error(err)
	}
	defer client.Close()
	client.AddConsumer(exchangeName, "topic", "test_launch", messaging.LaunchesKey, func(d amqp.Delivery) {
		d.Ack(false)
		launch := &messaging.JobRequest{}
		err = json.Unmarshal(d.Body, launch)
		if err != nil {
			t.Error(err)
		}
		actual := launch.Job.Description
		expected := job.Description
		if actual != expected {
			t.Errorf("Description was %s instead of %s", actual, expected)
		}
		actual = launch.Job.InvocationID
		expected = job.InvocationID
		if actual != expected {
			t.Errorf("InvocationID was %s instead of %s", actual, expected)
		}
		exitChan <- 1
	},
		0)
	client.SetupPublishing(exchangeName)
	go client.Listen()
	time.Sleep(100 * time.Millisecond)
	marshalledJob, err := json.Marshal(job)
	if err != nil {
		t.Error(err)
	}
	request, err := http.NewRequest("POST", "http://for-a-test.org/", bytes.NewReader(marshalledJob))
	if err != nil {
		t.Error(err)
	}
	recorder := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Errorf("launch() didn't return a 200 status code: %d", recorder.Code)
	}
	deltaQueueExists, err := client.QueueExists(messaging.TimeLimitDeltaQueueName(job.InvocationID))
	if err != nil {
		t.Error(err)
	}
	if !deltaQueueExists {
		t.Errorf("AMQP queue %s does not exist", messaging.TimeLimitDeltaQueueName(job.InvocationID))
	}
	requestQueueExists, err := client.QueueExists(messaging.TimeLimitRequestQueueName(job.InvocationID))
	if err != nil {
		t.Error(err)
	}
	if !requestQueueExists {
		t.Errorf("AMQP queue %s does not exist", messaging.TimeLimitRequestQueueName(job.InvocationID))
	}
	responseQueueExists, err := client.QueueExists(messaging.TimeLimitResponsesQueueName(job.InvocationID))
	if err != nil {
		t.Error(err)
	}
	if !responseQueueExists {
		t.Errorf("AMQP queue %s does not exist", messaging.TimeLimitResponsesQueueName(job.InvocationID))
	}
	stopQueueExists, err := client.QueueExists(messaging.StopQueueName(job.InvocationID))
	if err != nil {
		t.Error(err)
	}
	if !stopQueueExists {
		t.Errorf("AMQP queue %s does not exist", messaging.StopQueueName(job.InvocationID))
	}
	<-exitChan
}

func TestPreview(t *testing.T) {
	job := inittests(t)
	app := New(cfg)
	params := job.Steps[0].Config.Params
	previewer := &Previewer{
		Params: model.PreviewableStepParam(params),
	}
	marshalledPreviewer, err := json.Marshal(previewer)
	if err != nil {
		t.Error(err)
	}
	request, err := http.NewRequest("POST", "http://for-a-test.org/arg-preview", bytes.NewReader(marshalledPreviewer))
	if err != nil {
		t.Error(err)
	}
	recorder := httptest.NewRecorder()
	app.NewRouter().ServeHTTP(recorder, request)
	if recorder.Code != 200 {
		t.Errorf("preview() didn't return a 200 status code: %d", recorder.Code)
	}
	returnedPreview := &PreviewerReturn{}
	body, err := ioutil.ReadAll(recorder.Body)
	if err != nil {
		t.Error(err)
	}
	err = json.Unmarshal(body, returnedPreview)
	if err != nil {
		t.Error(err)
	}
	actual := returnedPreview.Params
	expected := model.PreviewableStepParam(params).String()
	if actual != expected {
		t.Errorf("param preview was %s instead of %s", actual, expected)
	}
}
