package outbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/IBM/sarama"
)

type TopicDurability struct {
	Topic           string
	Replicas        int // the fewest replicas of any partition
	MinInsync       int // min.insync.replicas
	UncleanElection bool
	Err             error // the topic could not be described, for example without the DescribeConfigs ACL
}

// Weak reports a topic on which acks=all can still lose a record: with fewer than 3 replicas, a
// min.insync.replicas below 2 or unclean leader election, the in-sync set can shrink to the leader alone.
func (d TopicDurability) Weak() bool {
	return d.Err == nil && (d.Replicas < 3 || d.MinInsync < 2 || d.UncleanElection)
}

// Describer is the part of sarama.ClusterAdmin that the durability check uses.
type Describer interface {
	DescribeTopics(topics []string) ([]*sarama.TopicMetadata, error)
	DescribeConfig(resource sarama.ConfigResource) ([]sarama.ConfigEntry, error)
}

// CheckDurability describes the topics through admin, at most parallel at a time. It only reads. A topic not
// described when ctx ends gets ctx's error.
func CheckDurability(ctx context.Context, admin Describer, topics []string, parallel int) []TopicDurability {
	type result struct {
		i int
		d TopicDurability
	}

	results := make(chan result, len(topics))
	slots := make(chan struct{}, max(parallel, 1))
	go func() {
		for i, topic := range topics {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				defer func() { <-slots }()
				results <- result{i: i, d: describeDurability(admin, topic)}
			}()
		}
	}()

	out := make([]TopicDurability, len(topics))
	done := make([]bool, len(topics))
	for range topics {
		select {
		case r := <-results:
			out[r.i], done[r.i] = r.d, true
		case <-ctx.Done():
			for i, topic := range topics {
				if !done[i] {
					out[i] = TopicDurability{Topic: topic, Err: ctx.Err()}
				}
			}

			return out
		}
	}

	return out
}

func describeDurability(admin Describer, topic string) TopicDurability {
	d := TopicDurability{Topic: topic}

	metadata, err := admin.DescribeTopics([]string{topic})
	if err == nil && (len(metadata) != 1 || metadata[0] == nil) {
		err = errors.New("no metadata")
	}
	if err == nil && !errors.Is(metadata[0].Err, sarama.ErrNoError) {
		err = metadata[0].Err
	}
	if err == nil && len(metadata[0].Partitions) == 0 {
		err = errors.New("no partitions")
	}
	if err != nil {
		d.Err = fmt.Errorf("describe topic %s: %w", topic, err)

		return d
	}
	d.Replicas = len(metadata[0].Partitions[0].Replicas)
	for _, p := range metadata[0].Partitions {
		d.Replicas = min(d.Replicas, len(p.Replicas))
	}

	entries, err := admin.DescribeConfig(sarama.ConfigResource{
		Type:        sarama.TopicResource,
		Name:        topic,
		ConfigNames: []string{"min.insync.replicas", "unclean.leader.election.enable"},
	})
	if err != nil {
		d.Err = fmt.Errorf("describe the config of topic %s: %w", topic, err)

		return d
	}

	d.MinInsync = 1 // Kafka's default, also when the broker does not report it
	for _, entry := range entries {
		switch entry.Name {
		case "min.insync.replicas":
			if d.MinInsync, err = strconv.Atoi(entry.Value); err != nil {
				d.Err = fmt.Errorf("min.insync.replicas of topic %s: %w", topic, err)
			}
		case "unclean.leader.election.enable":
			d.UncleanElection = entry.Value == "true"
		}
	}

	return d
}

// ReportDurability logs each weak topic at Error and each one it could not check at Warn, and sets
// Metrics.WeakTopics to the topics not known to be durable, the weak and the unchecked ones, or to -1 when it
// checked none: no topic, a nil admin (a check that could not start), or every topic failed. It never fails: a
// weak topic still works, as with acks=1.
func (o *Outbox) ReportDurability(ctx context.Context, admin Describer, topics []string, parallel int,
) []TopicDurability {
	if admin == nil || len(topics) == 0 {
		o.metrics.WeakTopics(-1)
		o.log.Warn("outbox: the durability of the kafka topics was not checked", "topics", len(topics))

		return nil
	}

	checked := CheckDurability(ctx, admin, topics, parallel)

	weak, unchecked := 0, 0
	for _, d := range checked {
		if d.Err != nil {
			unchecked++
			o.log.Warn("outbox: could not check the durability of a kafka topic", "topic", d.Topic, "error", d.Err)

			continue
		}
		if d.Weak() {
			weak++
			o.log.Error("outbox: acks=all is weak on this topic, a broker loss can stop or lose records the outbox "+
				"already deleted: it needs 3 replicas, min.insync.replicas 2 and no unclean leader election",
				"topic", d.Topic, "replicas", d.Replicas, "min_insync_replicas", d.MinInsync,
				"unclean_leader_election", d.UncleanElection)
		}
	}
	notDurable := weak + unchecked
	if unchecked == len(checked) {
		notDurable = -1
	}
	o.metrics.WeakTopics(notDurable)
	o.log.Info("outbox: kafka durability checked", "topics", topics, "weak", weak, "unchecked", unchecked)

	return checked
}
