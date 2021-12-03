package SocketIO

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/garyburd/redigo/redis"
	"github.com/sirupsen/logrus"
	"github.com/vmihailenco/msgpack"
)

const (
	UID          = "emitter"
	EVENT        = 2
	BINARY_EVENT = 5

	redisPoolMaxIdle   = 80
	redisPoolMaxActive = 10000 // max number of connections
)

type EmitterOpts struct {
	// Host means hostname like localhost
	Host string
	// Port means port number, like 6379
	Port int
	// Key means redis subscribe key
	Key string
	// Protocol, like tcp
	Protocol string
	// Address, like localhost:6379
	Addr string
}

type BroadcastOpts struct {
	nsp              string
	broadcastChannel string
	requestChannel   string
}

type Emitter struct {
	Key       string
	rooms     []string
	flags     map[string]interface{}
	redisPool *redis.Pool

	*BroadcastOpts
}

func initRedisConnPool(opts *EmitterOpts) *redis.Pool {
	if opts.Host == "" {
		// return err
	}

	var addr string
	if opts.Addr != "" {
		addr = opts.Addr
	} else if opts.Host != "" && opts.Port > 0 {
		addr = opts.Host + ":" + strconv.Itoa(opts.Port)
	} else {
		addr = "localhost:6379"
	}

	return &redis.Pool{
		MaxIdle:   redisPoolMaxIdle,
		MaxActive: redisPoolMaxActive,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", addr)
			if err != nil {
				logrus.Errorf("Failed to init redis pool: %+v", err)
				return nil, err
			}

			// TODO: passwd check if needed

			return c, nil
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}

// Emitter constructor
// Usage:
// SocketIO.NewEmitter(&SocketIO.EmitterOpts{
//    Host:"localhost",
//    Port:6379,
// })
func NewEmitter(opts *EmitterOpts) (*Emitter, error) {
	if opts.Key == "" {
		opts.Key = "socket.io"
	}

	nsp := "/"
	broadcastOpts := &BroadcastOpts{
		broadcastChannel: fmt.Sprintf("%s#%s#", opts.Key, nsp),
		requestChannel:   fmt.Sprintf("%s-request#%s#", opts.Key, nsp),
	}

	emitter := &Emitter{
		Key:           opts.Key,
		redisPool:     initRedisConnPool(opts),
		BroadcastOpts: broadcastOpts,
	}
	return emitter, nil
}

func (emitter *Emitter) Join() *Emitter {
	emitter.flags["join"] = true
	return emitter
}

func (emitter *Emitter) Volatile() *Emitter {
	emitter.flags["volatile"] = true
	return emitter
}

func (emitter *Emitter) Broadcast() *Emitter {
	emitter.flags["broadcast"] = true
	return emitter
}

/**
 * Limit emission to a certain `room`.
 *
 * @param {String} room
 */
func (emitter *Emitter) In(room string) *Emitter {
	for _, r := range emitter.rooms {
		if r == room {
			return emitter
		}
	}
	emitter.rooms = append(emitter.rooms, room)
	return emitter
}

func (emitter *Emitter) To(room string) *Emitter {
	return emitter.In(room)
}

/**
 * Limit emission to certain `namespace`.
 *
 * @param {String} namespace
 */
func (emitter *Emitter) Of(namespace string) *Emitter {
	emitter.flags["nsp"] = namespace
	return emitter
}

// send the packet by string, json, etc
// Usage:
// Emit("event name", "data")
func (emitter *Emitter) Emit(event string, data ...interface{}) (*Emitter, error) {
	d := []interface{}{event}
	d = append(d, data...)
	eventType := EVENT
	if HasBinary(data...) {
		eventType = BINARY_EVENT
	}
	packet := map[string]interface{}{
		"type": eventType,
		"data": d,
	}
	return emitter.emit(packet)
}

// send the packet by binary
// Usage:
// EmitBinary("event name", []byte{0x01, 0x02, 0x03})
func (emitter *Emitter) EmitBinary(event string, data ...interface{}) (*Emitter, error) {
	d := []interface{}{event}
	d = append(d, data...)
	packet := map[string]interface{}{
		"type": BINARY_EVENT,
		"data": d,
	}
	return emitter.emit(packet)
}

func HasBinary(dataSlice ...interface{}) bool {
	if dataSlice == nil {
		return false
	}
	for _, data := range dataSlice {
		switch dataType := data.(type) {
		case []byte:
			return true
		case bytes.Buffer:
			return true
		case []interface{}:
			for _, d := range dataType {
				result := HasBinary(d)
				if result {
					return true
				}
			}
		case map[string]interface{}:
			for _, v := range dataType {
				result := HasBinary(v)
				if result {
					return true
				}
			}
		default:
			return false
		}
	}
	return false
}

func (e *Emitter) publish(channel string, buf *bytes.Buffer) {
	c := e.redisPool.Get()
	defer c.Close()
	_, err := c.Do("PUBLISH", channel, buf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %+v", err)
	}
}

func (emitter *Emitter) emit(packet map[string]interface{}) (*Emitter, error) {
	if emitter.flags["nsp"] != nil {
		packet["nsp"] = emitter.flags["nsp"]
		delete(emitter.flags, "nsp")
	}
	var pack []interface{} = make([]interface{}, 0)
	pack = append(pack, UID)
	pack = append(pack, packet)
	pack = append(pack, map[string]interface{}{
		"rooms": emitter.rooms,
		"flags": emitter.flags,
	})
	buf := &bytes.Buffer{}
	enc := msgpack.NewEncoder(buf)
	error := enc.Encode(pack)
	if error != nil {
		return nil, error
	}

	emitter.flags = make(map[string]interface{})

	channel := emitter.BroadcastOpts.broadcastChannel
	if len(emitter.rooms) == 1 {
		channel = fmt.Sprintf("%s%s#", emitter.broadcastChannel, emitter.rooms[0])
	}

	emitter.publish(channel, buf)
	return emitter, nil
}
