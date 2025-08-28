package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/IBM/sarama"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
)

type mockKafkaServer struct {
	container *kafka.KafkaContainer
	producer  sarama.SyncProducer
	topics    map[string][]any
	mu        sync.RWMutex
}

func setupMockKafkaServer(t *testing.T) *mockKafkaServer {
	t.Helper()

	ctx := context.Background()

	// Start Kafka container using Testcontainers
	kafkaContainer, err := kafka.Run(ctx,
		"confluentinc/confluent-local:7.5.0",
		kafka.WithClusterID("test-cluster"),
	)
	if err != nil {
		t.Fatalf("Failed to start Kafka container: %v", err)
	}

	// Get broker addresses
	brokers, err := kafkaContainer.Brokers(ctx)
	if err != nil {
		t.Fatalf("Failed to get Kafka brokers: %v", err)
	}

	// Create a sync producer for sending messages
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Retry.Max = 5
	config.Version = sarama.V2_8_1_0

	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		t.Fatalf("Failed to create producer: %v", err)
	}

	mockServer := &mockKafkaServer{
		container: kafkaContainer,
		producer:  producer,
		topics:    make(map[string][]any),
	}

	return mockServer
}

// PushMessageToTopic sends a message to a specific topic
func (m *mockKafkaServer) PushMessageToTopic(topic string, payload []byte, headers map[string]string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Create producer message
	msg := &sarama.ProducerMessage{
		Topic: topic,
		Value: sarama.ByteEncoder(payload),
	}

	for key, value := range headers {
		msg.Headers = append(msg.Headers, sarama.RecordHeader{
			Key:   []byte(key),
			Value: []byte(value),
		})
	}

	// Send message
	_, _, err := m.producer.SendMessage(msg)
	if err != nil {
		return fmt.Errorf("failed to send message to topic %s: %w", topic, err)
	}

	// Store in our internal topic storage for testing purposes
	if m.topics[topic] == nil {
		m.topics[topic] = make([]any, 0)
	}
	m.topics[topic] = append(m.topics[topic], string(payload))

	return nil
}

// PushJSONToTopic sends a JSON payload to a specific topic
func (m *mockKafkaServer) PushJSONToTopic(topic string, payload any) error {
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	return m.PushMessageToTopic(topic, jsonBytes, nil)
}

// GetMessagesFromTopic returns all messages for a specific topic (from internal storage)
func (m *mockKafkaServer) GetMessagesFromTopic(topic string) []any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if messages, exists := m.topics[topic]; exists {
		// Return a copy to avoid race conditions
		result := make([]any, len(messages))
		copy(result, messages)
		return result
	}
	return []any{}
}

// ClearTopic removes all messages from a specific topic (from internal storage)
func (m *mockKafkaServer) ClearTopic(topic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.topics, topic)
}

// GetBrokerAddress returns the first broker address as a string (for backward compatibility)
func (m *mockKafkaServer) GetBrokerAddress(t *testing.T) string {
	brokers, err := m.container.Brokers(t.Context())
	if err != nil {
		t.Fatalf("Failed to get Kafka brokers: %v", err)
	}
	if len(brokers) > 0 {
		return brokers[0]
	}
	t.Fatalf("No brokers found")
	return ""
}

// Close closes the mock Kafka server and cleans up resources
func (m *mockKafkaServer) Close() error {
	if m.producer != nil {
		_ = m.producer.Close()
	}
	if m.container != nil {
		return m.container.Terminate(context.Background())
	}
	return nil
}
