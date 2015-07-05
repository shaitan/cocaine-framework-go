package cocaine12

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"golang.org/x/net/context"
)

func TestWorkerV1(t *testing.T) {
	const (
		testID      = "uuid"
		testSession = 10
	)

	var (
		onStop = make(chan struct{})
	)

	in, out := testConn()
	sock, _ := newAsyncRW(out)
	sock2, _ := newAsyncRW(in)
	w, err := newWorker(sock, testID, 1, true)
	if err != nil {
		t.Fatal("unable to create worker", err)
	}

	handlers := map[string]EventHandler{
		"test": func(ctx context.Context, req Request, res Response) {
			data, _ := req.Read()
			t.Logf("Request data: %s", data)
			res.Write(data)
			res.Close()
		},
		"error": func(ctx context.Context, req Request, res Response) {
			_, _ = req.Read()
			res.ErrorMsg(-100, "dummyError")
		},
		"http": WrapHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, method, r.Method)
			assert.Equal(t, "HTTP/"+version, r.Proto)
			assert.Equal(t, r.URL.String(), uri)
			assert.Equal(t, headersCocaineToHTTP(headers), r.Header)
			w.Header().Add("X-Test", "Test")
			w.WriteHeader(http.StatusProxyAuthRequired)
			fmt.Fprintf(w, "OK")
		}),
		"panic": func(ctx context.Context, req Request, res Response) {
			panic("PANIC")
		},
	}

	go func() {
		w.Run(handlers)
		close(onStop)
	}()

	corrupted := newInvokeV1(testSession-1, "AAA")
	corrupted.Payload = []interface{}{nil}
	sock2.Write() <- corrupted

	sock2.Write() <- newInvokeV1(testSession, "test")
	sock2.Write() <- newChunkV1(testSession, []byte("Dummy"))
	sock2.Write() <- newChokeV1(testSession)

	sock2.Write() <- newInvokeV1(testSession+1, "http")
	sock2.Write() <- newChunkV1(testSession+1, packTestReq(req))
	sock2.Write() <- newChokeV1(testSession + 1)

	sock2.Write() <- newInvokeV1(testSession+2, "error")
	sock2.Write() <- newChunkV1(testSession+2, []byte("Dummy"))
	sock2.Write() <- newChokeV1(testSession + 2)

	sock2.Write() <- newInvokeV1(testSession+3, "BadEvent")
	sock2.Write() <- newChunkV1(testSession+3, []byte("Dummy"))
	sock2.Write() <- newChokeV1(testSession + 3)

	sock2.Write() <- newInvokeV1(testSession+4, "panic")
	sock2.Write() <- newChunkV1(testSession+4, []byte("Dummy"))
	sock2.Write() <- newChokeV1(testSession + 4)

	// handshake
	eHandshake := <-sock2.Read()
	checkTypeAndSession(t, eHandshake, v1UtilitySession, v1Handshake)

	switch uuid := eHandshake.Payload[0].(type) {
	case string:
		if uuid != testID {
			t.Fatal("bad uuid")
		}
	case []uint8:
		if string(uuid) != testID {
			t.Fatal("bad uuid")
		}
	default:
		t.Fatal("no uuid")
	}

	eHeartbeat := <-sock2.Read()
	checkTypeAndSession(t, eHeartbeat, v1UtilitySession, v1Heartbeat)

	// test event
	eChunk := <-sock2.Read()
	checkTypeAndSession(t, eChunk, testSession, v1Write)
	assert.Equal(t, []byte("Dummy"), eChunk.Payload[0])
	eChoke := <-sock2.Read()
	checkTypeAndSession(t, eChoke, testSession, v1Close)

	// http event
	// status code & headers
	t.Log("HTTP test:")
	eChunk = <-sock2.Read()
	checkTypeAndSession(t, eChunk, testSession+1, v1Write)
	var firstChunk struct {
		Status  int
		Headers [][2]string
	}
	assert.NoError(t, testUnpackHttpChunk(eChunk.Payload, &firstChunk))
	assert.Equal(t, http.StatusProxyAuthRequired, firstChunk.Status, "http: invalid status code")
	assert.Equal(t, [][2]string{[2]string{"X-Test", "Test"}}, firstChunk.Headers, "http: headers")
	// body
	t.Log("Body check")
	eChunk = <-sock2.Read()
	checkTypeAndSession(t, eChunk, testSession+1, v1Write)
	assert.Equal(t, eChunk.Payload[0].([]byte), []byte("OK"), "http: invalid body %s", eChunk.Payload[0])
	eChoke = <-sock2.Read()
	checkTypeAndSession(t, eChoke, testSession+1, v1Close)

	// error event
	t.Log("error event")
	eError := <-sock2.Read()
	checkTypeAndSession(t, eError, testSession+2, v1Error)
	eChoke = <-sock2.Read()
	checkTypeAndSession(t, eChoke, testSession+2, v1Close)

	// badevent
	t.Log("badevent event")
	eError = <-sock2.Read()
	checkTypeAndSession(t, eError, testSession+3, v1Error)
	eChoke = <-sock2.Read()
	checkTypeAndSession(t, eChoke, testSession+3, v1Close)

	// panic
	t.Log("panic event")
	eError = <-sock2.Read()
	checkTypeAndSession(t, eError, testSession+4, v1Error)
	eChoke = <-sock2.Read()
	checkTypeAndSession(t, eChoke, testSession+4, v1Close)
	<-onStop
	w.Stop()
}

func TestWorkerV1Termination(t *testing.T) {
	const (
		testID = "uuid"
	)

	var onStop = make(chan struct{})

	in, out := testConn()
	sock, _ := newAsyncRW(out)
	sock2, _ := newAsyncRW(in)
	w, err := newWorker(sock, testID, 1, true)
	if err != nil {
		t.Fatal("unable to create worker", err)
	}

	go func() {
		w.Run(map[string]EventHandler{})
		close(onStop)
	}()

	eHandshake := <-sock2.Read()
	checkTypeAndSession(t, eHandshake, v1UtilitySession, v1Handshake)
	eHeartbeat := <-sock2.Read()
	checkTypeAndSession(t, eHeartbeat, v1UtilitySession, v1Heartbeat)

	sock2.Write() <- newHeartbeatV1()

	terminate := &Message{
		CommonMessageInfo: CommonMessageInfo{
			Session: v1UtilitySession,
			MsgType: v1Terminate,
		},
		Payload: []interface{}{100, "TestTermination"},
	}

	corrupted := &Message{
		CommonMessageInfo: CommonMessageInfo{
			Session: v1UtilitySession,
			MsgType: 9999,
		},
		Payload: []interface{}{100, "TestTermination"},
	}

	sock2.Write() <- corrupted

	select {
	case <-onStop:
		// an unexpected disown exit
		t.Fatalf("unexpected exit")
	case <-time.After(heartbeatTimeout + time.Second):
		t.Fatalf("unexpected timeout")
	case eHeartbeat := <-sock2.Read():
		checkTypeAndSession(t, eHeartbeat, v1UtilitySession, v1Heartbeat)
	}

	sock2.Write() <- terminate

	select {
	case <-onStop:
		// a termination exit
	case <-time.After(disownTimeout):
		t.Fatalf("unexpected exit")
	}
}
