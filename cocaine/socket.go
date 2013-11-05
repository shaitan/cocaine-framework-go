package cocaine

import (
	"log"
	"net"
	"time"
)

type AsyncIO interface {
	Read() chan RawMessage
	Write() chan RawMessage
	Close()
}

type AsyncBuff struct {
	in, out chan RawMessage
	stop    chan bool
}

func NewAsyncBuf() *AsyncBuff {
	buf := AsyncBuff{make(chan RawMessage), make(chan RawMessage), make(chan bool, 1)}
	buf.loop()
	return &buf
}

func (bf *AsyncBuff) loop() {
	go func() {
		var pending []RawMessage // data buffer
		var _in chan RawMessage  // incoming channel
		_in = bf.in

		finished := false // flag
		for {
			var first RawMessage
			var _out chan RawMessage
			if len(pending) > 0 {
				first = pending[0]
				_out = bf.out
			} else if finished {
				break
			}
			select {
			case incoming, ok := <-_in:
				if ok {
					pending = append(pending, incoming)
				} else {
					finished = true
					_in = nil
				}
			case _out <- first:
				pending[0] = nil
				pending = pending[1:]
			case <-bf.stop:
				close(bf.out) // Notify receiver
				return
			}
		}
	}()
}

func (bf *AsyncBuff) Stop() (res bool) {
	select {
	case bf.stop <- true:
		res = true
		// default:
		// 	res = false
	}
	return
}

type ASocket struct {
	net.Conn
	clientToSock   *AsyncBuff
	socketToClient *AsyncBuff
}

func NewASocket(family string, address string, timeout time.Duration) (*ASocket, error) {
	conn, err := net.DialTimeout(family, address, timeout)
	if err != nil {
		return nil, err
	}

	sock := ASocket{conn, NewAsyncBuf(), NewAsyncBuf()}
	sock.readloop()
	sock.writeloop()
	return &sock, nil
}

func (sock *ASocket) Close() {
	log.Println(sock.Conn.Close())
	log.Println(sock.clientToSock.Stop())
	log.Println(sock.socketToClient.Stop())
}

func (sock *ASocket) Write() chan RawMessage {
	return sock.clientToSock.in
}

func (sock *ASocket) Read() chan RawMessage {
	return sock.socketToClient.out
}

func (sock *ASocket) writeloop() {
	go func() {
		for incoming := range sock.clientToSock.out {
			_, err := sock.Conn.Write(incoming) //Add check for sending full
			if err != nil {
				break
			}
		}
	}()
}

func (sock *ASocket) readloop() {
	go func() {
		buf := make([]byte, 2048)
		for {
			count, err := sock.Conn.Read(buf)
			if err != nil {
				close(sock.socketToClient.in)
				break
			} else {
				bufferToSend := make([]byte, count)
				copy(bufferToSend[:], buf[:count])
				sock.socketToClient.in <- bufferToSend
			}
		}
	}()
}