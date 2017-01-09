package simpletransport

import (
	"io/ioutil"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHTTP(t *testing.T) {
	transport := &SimpleTransport{}
	req, _ := http.NewRequest("GET", "http://beautifulopen.com/", nil)
	res, _ := transport.RoundTrip(req)
	body, _ := ioutil.ReadAll(res.Body)

	res.Body.Close()

	if string(body) == "" {
		t.Error("Body is empty")
	}
}

func TestHTTPS(t *testing.T) {
	transport := &SimpleTransport{}
	req, _ := http.NewRequest("GET", "https://google.com", nil)
	res, _ := transport.RoundTrip(req)
	body, _ := ioutil.ReadAll(res.Body)

	res.Body.Close()

	if string(body) == "" {
		t.Error("Body is empty")
	}
}

func TestThrottle(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(2)

	transport := NewThrottleTransport(time.Second*3, time.Second*30, time.Second*30)

	go func(ttransport *ThrottleTransport) {
		defer wg.Done()
		req, _ := http.NewRequest("GET", "https://google.com", nil)
		res, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}

		body, _ := ioutil.ReadAll(res.Body)
		if string(body) == "" {
			t.Error("Body is empty")
		}
	}(transport)

	go func(transport *ThrottleTransport) {
		defer wg.Done()
		req, _ := http.NewRequest("GET", "https://google.com", nil)
		res, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}

		body, _ := ioutil.ReadAll(res.Body)
		if string(body) == "" {
			t.Error("Body is empty")
		}
	}(transport)

	wg.Wait()
}
