package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/juanefec/execs"
)

type Config struct {
	Reqs       map[string]*Req       `json:"reqs"`
	Executions map[string]*Execution `json:"executions"`
	Steps      []string              `json:"steps"` // ["req-id/exec-id", "req-id/exec-id"]
}

type Req struct {
	Url     string              `json:"url"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
	Body    json.RawMessage     `json:"body"`
}

type Execution struct {
	RPS      int `json:"rps"`
	Duration int `json:"duration"`
	Fire     func(interface{})
	Kill     func()
}

const (
	CONFIG_FILENAME = "config.json"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	f, err := os.ReadFile(CONFIG_FILENAME)
	check(err)

	config := Config{}
	err = json.Unmarshal(f, &config)
	check(err)

	client := Client{
		c:            *http.DefaultClient,
		Reqs:         config.Reqs,
		Executions:   config.Executions,
		responsePipe: make(chan *Res),
		responses:    make([]*Res, 0),
	}

	stopClient := client.run()

	for i, _ := range client.Executions {
		client.Executions[i].Fire, client.Executions[i].Kill = execs.Executor(client.action, client.handle)
	}

	for _, step := range config.Steps {
		reqexe := strings.Split(step, "/")
		req, exe := client.Reqs[reqexe[0]], client.Executions[reqexe[1]]
		execs.TimedLoop(execs.Repeat(func(i int) {
			exe.Fire(req)
		}, time.Second/time.Duration(exe.RPS)), time.Duration(exe.Duration)*time.Second)
	}

	for i, _ := range client.Executions {
		client.Executions[i].Kill()
	}

	stopClient()

	statuses := map[int]int{}
	errorList := []error{}
	totalResponses := len(client.responses)
	timeAccumulation := time.Duration(0)
	for i := range client.responses {
		timeAccumulation += client.responses[i].elapsed
		if client.responses[i].res == nil {
			errorList = append(errorList, client.responses[i].err)
			continue
		}
		_, ok := statuses[client.responses[i].res.StatusCode]
		if ok {
			statuses[client.responses[i].res.StatusCode]++
		} else {
			statuses[client.responses[i].res.StatusCode] = 1

		}
	}

	fmt.Printf("Total requests: %v\n", len(client.responses))
	fmt.Printf("Total errors: %v\n", len(errorList))
	fmt.Printf("Average response time: %v\n", time.Duration(totalResponses/int(timeAccumulation)).Seconds())
	w := tabwriter.NewWriter(os.Stdout, 1, 1, 1, ' ', 0)
	fisrtRow, secondRow := "status: \t", "quantity: \t"
	for status, n := range statuses {
		fisrtRow = fmt.Sprintf("%v%v\t", fisrtRow, status)
		secondRow = fmt.Sprintf("%v%v\t", secondRow, n)
	}
	fmt.Fprintln(w, fisrtRow)
	fmt.Fprintln(w, secondRow)
	w.Flush()

}

type Client struct {
	c            http.Client
	Reqs         map[string]*Req
	Executions   map[string]*Execution
	responsePipe chan *Res
	responses    []*Res
}

type Res struct {
	res     *http.Response
	err     error
	elapsed time.Duration
}

func (c *Client) do(r *http.Request) *Res {
	n := time.Now()
	res, err := c.c.Do(r)
	elapsed := time.Since(n)
	return &Res{res, err, elapsed}
}

func (c *Client) action(i interface{}) interface{} {
	req := i.(*Req)
	r, err := http.NewRequest(req.Method, req.Url, bytes.NewReader(req.Body))
	if err != nil {
		return err
	}
	r.Header = req.Headers
	return c.do(r)
}

func (c *Client) handle(i interface{}) {
	res := i.(*Res)
	c.responsePipe <- res
}

func (c *Client) run() func() {
	end := make(chan struct{})
	go func() {
		for res := range c.responsePipe {
			c.responses = append(c.responses, res)
		}
		end <- struct{}{}
	}()
	return func() {
		close(c.responsePipe)
		<-end
	}
}
