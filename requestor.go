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
	ReqBursts  map[string]*ReqBurst  `json:"req_burst"`
	Executions map[string]*Execution `json:"executions"`
	Steps      []string              `json:"steps"` // ["<step_name>", "<execution_name>"]
}

type Req struct {
	Url     string              `json:"url"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
	Body    json.RawMessage     `json:"body"`
}

type Execution struct {
	Steps []string `json:"steps"`
}

type ReqBurst struct {
	RPM      int `json:"rpm"`
	Duration int `json:"duration"`
	Fire     func(interface{})
	Kill     func()
}

const (
	CONFIG_FILENAME = "config.json"
	NOT_LOADED_RUNE = '▒'
	LOADED_RUNE     = '█'

	EXEC_TYPE_SECUENTIAL = "sec"
	EXEC_TYPE_PARALLEL   = "par"
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
		ReqBursts:    config.ReqBursts,
		responsePipe: make(chan *Res),
		responses:    make([]*Res, 0),
	}

	stopClient := client.run()

	for i, _ := range client.ReqBursts {
		client.ReqBursts[i].Fire, client.ReqBursts[i].Kill = execs.Executor(client.action, client.handle)
	}

	progressBarFull := []rune(strings.Repeat(string(LOADED_RUNE), 40))
	progressBar := []rune(strings.Repeat(string(NOT_LOADED_RUNE), 40))

	// Start iterating steps
	start := time.Now()
	for j := 0; j < len(config.Steps)-1; j += 2 {
		name, step := config.Steps[j], config.Steps[j+1]
		exe := config.Executions[step]

		stepsLen := 0
		rcountLen := 0
		estimatedDuration := time.Duration(0)
		for i := 0; i < len(exe.Steps); i++ {
			if strings.HasPrefix(exe.Steps[i], "$") {
				continue
			}
			reqburst := exe.Steps[i]

			if len(step) > stepsLen {
				stepsLen = len(step)
			}
			rb := client.ReqBursts[strings.Split(reqburst, "/")[1]]
			interval := time.Minute / time.Duration(rb.RPM)
			duration := time.Duration(rb.Duration) * time.Second
			rcount := fmt.Sprint(int(duration / interval))
			if len(rcount) > rcountLen {
				rcountLen = len(rcount)
			}
			estimatedDuration += duration
		}
		stepPad := "%-" + fmt.Sprint(stepsLen) + "s"
		stepNPad := "%-" + fmt.Sprint(len(fmt.Sprint(len(exe.Steps)/2))) + "s"
		rcountPad := "%" + fmt.Sprint((rcountLen*2)+4) + "s"

		loaderStringLen := 0
		for i := 0; i < 4; i++ {
			s := fmt.Sprintf("Starting execution%v", strings.Repeat(".", i))
			loaderStringLen = len(s)
			fmt.Printf("\r%v", s)
			time.Sleep(time.Second / 2)
		}
		fmt.Printf("\r%v", strings.Repeat(" ", loaderStringLen))
		fmt.Printf("\rStep - %v. Approxima duration: %v\n", name, estimatedDuration)

		for i := 0; i < len(exe.Steps); i++ {
			reqburst := exe.Steps[i]
			//executionType := EXEC_TYPE_SECUENTIAL
			if strings.HasPrefix(reqburst, "$") {

				continue
			}
			reqexe := strings.Split(reqburst, "/")
			req, exe := client.Reqs[reqexe[0]], client.ReqBursts[reqexe[1]]
			interval := time.Minute / time.Duration(exe.RPM)
			duration := time.Duration(exe.Duration) * time.Second

			rcount := int(duration / interval)
			slog := fmt.Sprintf(" - #%v  %v", fmt.Sprintf(stepNPad, fmt.Sprint((i/2)+1)), fmt.Sprintf(stepPad, reqburst))

			var (
				logPipe = make(chan int)
				logEnd  = make(chan struct{}, 1)
			)
			go func() {
				lasti := 0
				for i := range logPipe {
					if i < rcount {
						progress := i * 40 / rcount
						progressBar[progress] = LOADED_RUNE
						if progress-1 >= 0 && progressBar[progress-1] == NOT_LOADED_RUNE {
							for j := 0; j < progress; j++ {
								if progressBar[j] == NOT_LOADED_RUNE {
									progressBar[j] = LOADED_RUNE
								}
							}
						}
						fmt.Printf("\r%v %v  %v", slog, fmt.Sprintf(rcountPad, fmt.Sprintf("[%v/~%v]", i, rcount)), string(progressBar))
						lasti++
					}
				}
				fmt.Printf("\r%v %v  %v", slog, fmt.Sprintf(rcountPad, fmt.Sprintf("[%v/~%v]", lasti, rcount)), string(progressBarFull))
				logEnd <- struct{}{}
			}()

			execs.TimedLoop(execs.Repeat(func(rb *ReqBurst, req *Req) func(i int) {
				return func(i int) {
					rb.Fire(req)
					logPipe <- i
				}
			}(exe, req), interval), duration)

			close(logPipe)
			<-logEnd
			fmt.Print("\n")
			progressBar = []rune(strings.Repeat(string(NOT_LOADED_RUNE), 40))
		}
	}

	for i, _ := range client.ReqBursts {
		client.ReqBursts[i].Kill()
	}

	fmt.Println()

	elapsed := time.Since(start)
	fmt.Printf("Elapsed: %v\n", elapsed)

	stopClient()

	statuses := map[int]int{}
	errorList := []error{}
	totalResponses := len(client.responses)
	timeAccumulation := time.Duration(0)
	maxElapsed, minElapsed := time.Duration(0), client.responses[0].elapsed
	for i := range client.responses {
		if client.responses[i].elapsed != 0 && client.responses[i].elapsed < minElapsed {
			minElapsed = client.responses[i].elapsed
		}
		if client.responses[i].elapsed > maxElapsed {
			maxElapsed = client.responses[i].elapsed
		}
		timeAccumulation = timeAccumulation + client.responses[i].elapsed
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

	welapsed := tabwriter.NewWriter(os.Stdout, 1, 1, 1, ' ', 0)
	fmt.Fprintf(welapsed, "\n - Request duration info - \n")
	fmt.Fprintf(welapsed, "Avg\t|%v\t|\n", time.Duration(int(timeAccumulation)/totalResponses))
	fmt.Fprintf(welapsed, "Min\t|%v\t|\n", minElapsed)
	fmt.Fprintf(welapsed, "Max\t|%v\t|\n", maxElapsed)
	welapsed.Flush()

	wstatus := tabwriter.NewWriter(os.Stdout, 1, 1, 1, ' ', 0)
	fisrtRow, secondRow := "Status\t|", "Count\t|"
	for status, n := range statuses {
		fisrtRow = fmt.Sprintf("%v%v\t|", fisrtRow, status)
		secondRow = fmt.Sprintf("%v%v\t|", secondRow, n)
	}
	fmt.Fprintf(wstatus, "\n - HTTP status code info - \n")
	fmt.Fprintln(wstatus, fisrtRow)
	fmt.Fprintln(wstatus, secondRow)
	wstatus.Flush()

}

type Client struct {
	c            http.Client
	Reqs         map[string]*Req
	ReqBursts    map[string]*ReqBurst
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
