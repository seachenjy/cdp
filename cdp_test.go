package cdp

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestError(t *testing.T) {
	client, err := NewBrower("http://localhost:9222", 5)
	if err != nil {
		t.Error(err)
	}
	urls := []string{
		"http://www.qq.com",
		"http://www.tencent.com",
		"http://www.sina.com",
		"http://www.jandan.net",
		"http://www.weixin.com",
		"http://www.163.com",
		"http://www.taobao.com",
		"http://www.4399.com",
	}
	wait := &sync.WaitGroup{}
	for _, url := range urls {
		wait.Add(1)
		fmt.Println("job start -> ", url)
		go func(u string, w *sync.WaitGroup) {
			client.Do(func(tab *Tab) {
				err := tab.Navigate(u)
				if err != nil {
					t.Error(err)
					return
				}
				ready := tab.WaitPageComplete(time.Second * 5)
				if !ready {
					t.Error("page load error")
					return
				}
				title, err := tab.Evaluate("document.title")
				if err != nil {
					t.Error(err)
					return
				}
				w.Done()
				t.Log(title, u)
			})
		}(url, wait)
	}

	wait.Wait()
}

func TestResponseBody(t *testing.T) {
	client, err := NewBrower("http://localhost:9222", 1)
	if err != nil {
		t.Error(err)
	}
	url := "https://www.wolai.com/downloads"
	client.Do(func(tab *Tab) {
		reqid := make(chan string)
		tab.CallbackEvent("Network.responseReceived", func(params Params) {
			response := params["response"].(map[string]interface{})
			if response["mimeType"].(string) == "application/json" {
				requestid := params["requestId"].(string)
				reqid <- requestid
			}
		})
		tab.NetworkEvents(true)
		tab.Navigate(url)
		for {
			select {
			case id := <-reqid:
				bytes, err := tab.GetResponseBody(id)
				if err == nil {
					t.Log(string(bytes))
				} else {
					t.Error(err)
				}
			case <-time.After(time.Second * 10):
				return
			}
		}
	})
	time.Sleep(30 * time.Second)
}
