// Package mqtt subscribes to topics on an IoT Open MQTT broker.
package mqtt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// DefaultPort is the MQTT over TLS port of IoT Open brokers.
const DefaultPort = "8883"

// Message is a received MQTT message.
type Message struct {
	Topic   string
	Payload []byte
}

// Options describe the connection.
type Options struct {
	// Broker is "host", "host:port" or a URL such as "tcp://localhost:1883".
	// Without a scheme, TLS on port 8883 is used.
	Broker string
	// Password is a session token or API key; the username is not checked
	// by the platform.
	Password string
}

// BrokerURL turns Options.Broker into a URL that Paho understands.
func BrokerURL(broker string) string {
	if strings.Contains(broker, "://") {
		return broker
	}
	if !strings.Contains(broker, ":") {
		broker += ":" + DefaultPort
	}
	return "ssl://" + broker
}

// Subscribe connects, subscribes to topics and calls handle for every
// message until ctx is cancelled. Lost connections are re-established and
// the subscriptions renewed.
func Subscribe(ctx context.Context, opts Options, topics []string, handle func(Message)) error {
	filters := make(map[string]byte, len(topics))
	for _, t := range topics {
		filters[t] = 0
	}

	co := paho.NewClientOptions()
	co.AddBroker(BrokerURL(opts.Broker))
	co.SetClientID("iotopenctl-" + randomSuffix())
	co.SetUsername("iotopenctl")
	co.SetPassword(opts.Password)
	co.SetCleanSession(true)
	co.SetAutoReconnect(true)
	co.SetConnectTimeout(15 * time.Second)
	co.SetOrderMatters(true)
	subErr := make(chan error, 1)
	co.SetOnConnectHandler(func(c paho.Client) {
		// Subscriptions do not survive a reconnect with a clean session.
		tok := c.SubscribeMultiple(filters, func(_ paho.Client, m paho.Message) {
			handle(Message{Topic: m.Topic(), Payload: m.Payload()})
		})
		go func() {
			if !tok.WaitTimeout(20 * time.Second) {
				return
			}
			err := tok.Error()
			if st, ok := tok.(*paho.SubscribeToken); ok && err == nil {
				for topic, code := range st.Result() {
					if code == 0x80 { // SUBACK failure: denied by the broker
						err = fmt.Errorf("the broker refused the subscription to %q", topic)
					}
				}
			}
			if err != nil {
				select {
				case subErr <- fmt.Errorf("subscribing: %w", err):
				default:
				}
			}
		}()
	})

	client := paho.NewClient(co)
	tok := client.Connect()
	if !tok.WaitTimeout(20 * time.Second) {
		return fmt.Errorf("connecting to %s: timed out", BrokerURL(opts.Broker))
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("connecting to %s: %w", BrokerURL(opts.Broker), err)
	}
	defer client.Disconnect(250)
	select {
	case <-ctx.Done():
		return nil
	case err := <-subErr:
		return err
	}
}

func randomSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
