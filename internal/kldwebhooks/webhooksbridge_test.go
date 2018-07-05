// Copyright 2018 Kaleido, a ConsenSys business

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kldwebhooks

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shopify/sarama"
	"github.com/kaleido-io/ethconnect/internal/kldkafka"
	"github.com/kaleido-io/ethconnect/internal/kldmessages"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
)

type testKafkaCommon struct {
	stop             chan bool
	startCalled      bool
	startErr         error
	cobraInitCalled  bool
	cobraPreRunError error
	kafkaFactory     *kldkafka.MockKafkaFactory
	kafkaInitDelay   int
	startTime        time.Time
}

func (k *testKafkaCommon) Start() error {
	k.startCalled = true
	log.Infof("Test KafkaCommon started")
	<-k.stop
	log.Infof("Test KafkaCommon stopped")
	if k.kafkaFactory.Consumer != nil {
		k.kafkaFactory.Consumer.Close()
	}
	if k.kafkaFactory.Producer != nil {
		k.kafkaFactory.Producer.AsyncClose()
	}
	return k.startErr
}

func (k *testKafkaCommon) CobraInit(cmd *cobra.Command) {
	k.cobraInitCalled = true
}

func (k *testKafkaCommon) CobraPreRunE(cmd *cobra.Command) error {
	return k.cobraPreRunError
}

func (k *testKafkaCommon) CreateTLSConfiguration() (t *tls.Config, err error) {
	return nil, nil
}

func (k *testKafkaCommon) Conf() *kldkafka.KafkaCommonConf {
	return &kldkafka.KafkaCommonConf{}
}

func (k *testKafkaCommon) Producer() kldkafka.KafkaProducer {
	var producer kldkafka.KafkaProducer
	timeSinceStart := time.Now().Sub(k.startTime)
	if timeSinceStart > time.Duration(k.kafkaInitDelay)*time.Millisecond {
		producer = k.kafkaFactory.Producer
	}
	return producer
}

var webhookExecuteError atomic.Value

func newTestKafkaComon() *testKafkaCommon {
	kafka := &testKafkaCommon{}
	kafka.startTime = time.Now()
	kafka.stop = make(chan bool)
	kafka.kafkaFactory = kldkafka.NewMockKafkaFactory()
	kafka.kafkaFactory.NewProducer(kafka)
	kafka.kafkaFactory.NewConsumer(kafka)
	return kafka
}

// startTestWebhooks creates a Webhooks instance with a Cobra command wrapper, and executes it
// It returns once it's reached kafka initialization successfully, or errored during initialization
func startTestWebhooks(testArgs []string, kafka *testKafkaCommon) (*WebhooksBridge, error) {
	log.SetLevel(log.DebugLevel)
	w := NewWebhooksBridge()
	w.kafka = kafka
	cmd := w.CobraInit()
	cmd.SetArgs(testArgs)
	webhookExecuteError.Store(errors.New("none"))
	go func() {
		err := cmd.Execute()
		log.Infof("Kafka webhooks completed. Err=%s", err)
		if err != nil {
			webhookExecuteError.Store(err)
		}
	}()
	status := -1
	var err error
	for err == nil && status != 200 {
		statusURL := fmt.Sprintf("http://localhost:%d/status", w.conf.Port)
		resp, httpErr := http.Get(statusURL)
		if httpErr == nil {
			status = resp.StatusCode
		}
		errI := webhookExecuteError.Load()
		if errI != nil {
			err = errI.(error)
		}
		log.Infof("Waiting for Webhook server to start (URL=%s Status=%d HTTPErr=%s Err=%s)", statusURL, status, httpErr, err)
		if status != 200 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err != nil && err.Error() == "none" {
		err = nil
	}
	return w, err
}

func TestStartStopDefaultArgs(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	w, err := startTestWebhooks([]string{}, k)
	assert.Nil(err)

	assert.Equal(8080, w.conf.Port)    // default
	assert.Equal("", w.conf.LocalAddr) // default

	k.stop <- true
}

func TestStartStopCustomArgs(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	w, err := startTestWebhooks([]string{"-l", "8081", "-L", "127.0.0.1"}, k)
	assert.Nil(err)

	assert.Equal(8081, w.conf.Port)
	assert.Equal("127.0.0.1", w.conf.LocalAddr)
	assert.Equal("127.0.0.1:8081", w.srv.Addr)

	k.stop <- true
}

func TestStartStopKafkaInitDelay(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	k.kafkaInitDelay = 500
	_, err := startTestWebhooks([]string{}, k)
	assert.Nil(err)

	k.stop <- true
}

func TestStartStopKafkaPreRunError(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	k.cobraPreRunError = fmt.Errorf("pop")
	_, err := startTestWebhooks([]string{}, k)
	assert.Errorf(err, "pop")
}

func TestConsumerMessagesLoopIsNoop(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	w, err := startTestWebhooks([]string{}, k)
	assert.Nil(err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	consumer, _ := k.kafkaFactory.NewConsumer(k)
	producer, _ := k.kafkaFactory.NewProducer(k)

	go func() {
		w.ConsumerMessagesLoop(consumer, producer, wg)
	}()

	consumer.(*kldkafka.MockKafkaConsumer).MockMessages <- &sarama.ConsumerMessage{
		Value: []byte("hello world"),
	}

	k.stop <- true
	wg.Wait()

}

func TestProducerErrorLoopIsNoop(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	w, err := startTestWebhooks([]string{}, k)
	assert.Nil(err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	consumer, _ := k.kafkaFactory.NewConsumer(k)
	producer, _ := k.kafkaFactory.NewProducer(k)

	go func() {
		w.ProducerErrorLoop(consumer, producer, wg)
	}()

	producer.(*kldkafka.MockKafkaProducer).MockErrors <- &sarama.ProducerError{
		Err: fmt.Errorf("fizzle"),
	}

	k.stop <- true
	wg.Wait()

}

func TestProducerSuccessesLoopIsNoop(t *testing.T) {
	assert := assert.New(t)

	k := newTestKafkaComon()
	w, err := startTestWebhooks([]string{}, k)
	assert.Nil(err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	consumer, _ := k.kafkaFactory.NewConsumer(k)
	producer, _ := k.kafkaFactory.NewProducer(k)

	go func() {
		w.ProducerSuccessLoop(consumer, producer, wg)
	}()

	producer.(*kldkafka.MockKafkaProducer).MockSuccesses <- &sarama.ProducerMessage{}

	k.stop <- true
	wg.Wait()

}

func assertOKResp(assert *assert.Assertions, resp *http.Response) {
	assert.Equal(200, resp.StatusCode)
	replyBytes, err := ioutil.ReadAll(resp.Body)
	assert.Nil(err)
	if resp.StatusCode == 200 {
		var replyMsg okMsg
		err = json.Unmarshal(replyBytes, &replyMsg)
		assert.Nil(err)
		assert.Equal(true, replyMsg.OK)
	} else {
		var replyMsg errMsg
		err = json.Unmarshal(replyBytes, &replyMsg)
		assert.Nil(err)
		log.Errorf("Error from server: %s", replyMsg.Message)
	}
}

func assertErrResp(assert *assert.Assertions, resp *http.Response, status int, msg string) {
	assert.Equal(status, resp.StatusCode)
	replyBytes, err := ioutil.ReadAll(resp.Body)
	assert.Nil(err)
	var replyMsg errMsg
	err = json.Unmarshal(replyBytes, &replyMsg)
	assert.Nil(err)
	assert.Regexp(msg, replyMsg.Message)
}
func sendTestTransaction(assert *assert.Assertions, msgBytes []byte, contentType string) (*http.Response, [][]byte) {

	k := newTestKafkaComon()
	w, err := startTestWebhooks([]string{}, k)
	assert.Nil(err)

	wg := &sync.WaitGroup{}
	wg.Add(1)
	var msgs [][]byte
	go func() {
		for msg := range k.kafkaFactory.Producer.MockInput {
			msgBytes, _ := msg.Value.Encode()
			log.Infof("Message sent by webhook bridge: %s", string(msgBytes))
			msgs = append(msgs, msgBytes)
		}
		wg.Done()
	}()

	url := fmt.Sprintf("http://localhost:%d/message", w.conf.Port)
	resp, httpErr := http.Post(url, contentType, bytes.NewReader(msgBytes))
	if err != nil {
		log.Errorf("HTTP error for %s: %+v", url, err)
	}
	assert.Nil(httpErr)

	k.stop <- true
	wg.Wait()

	return resp, msgs
}

func TestWebhookHandlerJSONSendTransaction(t *testing.T) {

	assert := assert.New(t)

	msg := kldmessages.SendTransaction{}
	msg.Headers.MsgType = kldmessages.MsgTypeSendTransaction
	msgBytes, _ := json.Marshal(&msg)
	resp, replyMsgs := sendTestTransaction(assert, msgBytes, "application/json")
	assertOKResp(assert, resp)
	assert.Equal(1, len(replyMsgs))

	forwardedMessage := kldmessages.SendTransaction{}
	json.Unmarshal(replyMsgs[0], &forwardedMessage)
	assert.Equal(kldmessages.MsgTypeSendTransaction, forwardedMessage.Headers.MsgType)
}

func TestWebhookHandlerJSONDeployContract(t *testing.T) {

	assert := assert.New(t)

	msg := kldmessages.DeployContract{}
	msg.Headers.MsgType = kldmessages.MsgTypeDeployContract
	msg.From = "any string"
	msgBytes, _ := json.Marshal(&msg)
	resp, replyMsgs := sendTestTransaction(assert, msgBytes, "application/json")
	assertOKResp(assert, resp)
	assert.Equal(1, len(replyMsgs))

	forwardedMessage := kldmessages.SendTransaction{}
	json.Unmarshal(replyMsgs[0], &forwardedMessage)
	assert.Equal(kldmessages.MsgTypeDeployContract, forwardedMessage.Headers.MsgType)
}

func TestWebhookHandlerYAMLDeployContract(t *testing.T) {

	assert := assert.New(t)

	msg := "" +
		"headers:\n" +
		"  type: DeployContract\n" +
		"from: '0x4b098809E68C88e26442491c57866b7D4852216c'\n" +
		"solidity: |-\n" +
		"  pragma solidity ^0.4.17;\n" +
		"  \n" +
		"  contract simplestorage {\n" +
		"    uint public storedData;\n" +
		"  \n" +
		"    function simplestorage(uint initVal) public {\n" +
		"      storedData = initVal;\n" +
		"    }\n" +
		"\n" +
		"    function set(uint x) public {\n" +
		"      storedData = x;\n" +
		"    }\n" +
		"    \n" +
		"    function get() public constant returns (uint retVal) {\n" +
		"      return storedData;\n" +
		"    }\n" +
		"  }\n" +
		"\n"

	resp, replyMsgs := sendTestTransaction(assert, []byte(msg), "application/x-yaml")
	assertOKResp(assert, resp)
	assert.Equal(1, len(replyMsgs))

	forwardedMessage := kldmessages.SendTransaction{}
	json.Unmarshal(replyMsgs[0], &forwardedMessage)
	assert.Equal(kldmessages.MsgTypeDeployContract, forwardedMessage.Headers.MsgType)
}

func TestWebhookHandlerYAMLBadHeaders(t *testing.T) {

	assert := assert.New(t)

	msg := "" +
		"headers: some string" +
		"\n"

	resp, replyMsgs := sendTestTransaction(assert, []byte(msg), "application/x-yaml")
	assertErrResp(assert, resp, 400, "Invalid message - missing 'headers' \\(or not an object\\)")
	assert.Equal(0, len(replyMsgs))
}

func TestWebhookHandlerYAMLMissingType(t *testing.T) {

	assert := assert.New(t)

	msg := "" +
		"headers:\n" +
		"  type:\n" +
		"    an: object\n" +
		"\n"

	resp, replyMsgs := sendTestTransaction(assert, []byte(msg), "application/x-yaml")
	assertErrResp(assert, resp, 400, "Invalid message - missing 'headers.type' \\(or not a string\\)")
	assert.Equal(0, len(replyMsgs))
}

func TestWebhookHandlerYAMLMissingTo(t *testing.T) {

	assert := assert.New(t)

	msg := "" +
		"headers:\n" +
		"  type: DeployContract\n" +
		"\n"

	resp, replyMsgs := sendTestTransaction(assert, []byte(msg), "application/x-yaml")
	assertErrResp(assert, resp, 400, "Invalid message - missing 'from' \\(or not a string\\)")
	assert.Equal(0, len(replyMsgs))
}

func TestWebhookHandlerBadYAML(t *testing.T) {

	assert := assert.New(t)

	resp, replyMsgs := sendTestTransaction(assert, []byte("!badness!"), "application/x-yaml")
	assertErrResp(assert, resp, 400, "Unable to parse YAML")
	assert.Equal(0, len(replyMsgs))
}

func TestWebhookHandlerBadJSON(t *testing.T) {

	assert := assert.New(t)

	resp, replyMsgs := sendTestTransaction(assert, []byte("badness"), "application/json")
	assertErrResp(assert, resp, 400, "Unable to parse JSON")
	assert.Equal(0, len(replyMsgs))
}

func TestWebhookHandlerBadMsgType(t *testing.T) {

	assert := assert.New(t)

	msg := kldmessages.RequestCommon{}
	msg.Headers.MsgType = "badness"
	msgBytes, _ := json.Marshal(&msg)
	resp, replyMsgs := sendTestTransaction(assert, msgBytes, "application/json")
	assertErrResp(assert, resp, 400, "Invalid message type")
	assert.Equal(0, len(replyMsgs))
}

func TestWebhookHandlerTooBig(t *testing.T) {

	assert := assert.New(t)

	// Build a 1MB payload
	msgBytes := make([]byte, 1024*1024)
	resp, replyMsgs := sendTestTransaction(assert, msgBytes, "application/json")
	assertErrResp(assert, resp, 400, "Message exceeds maximum allowable size")
	assert.Equal(0, len(replyMsgs))
}
