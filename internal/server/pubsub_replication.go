package server

import "snugkv/internal/persistence"

// replicatePublish forwards a PUBLISH to replicas, which hand it to their own
// subscribers. Nothing is stored: the message exists only in the replication
// stream, as with Redis.
func (s *Server) replicatePublish(channel, message []byte, shard bool) {
	// A replica's own offsets must keep matching its upstream's, so only a
	// primary adds to the replication stream.
	if s.replication.isReadOnlyReplica() {
		return
	}
	s.publishReplication([]persistence.Record{{Publish: &persistence.PublishEvent{
		Channel: append([]byte(nil), channel...),
		Message: append([]byte(nil), message...),
		Shard:   shard,
	}}})
}

// deliverReplicatedPublishes hands the Pub/Sub messages in records to local
// subscribers and returns the records that remain to be applied to the data.
func (s *Server) deliverReplicatedPublishes(records []persistence.Record) []persistence.Record {
	has := false
	for i := range records {
		if records[i].Publish != nil {
			has = true
			break
		}
	}
	if !has {
		return records
	}
	hub := pubSubHubForServer(s)
	rest := make([]persistence.Record, 0, len(records))
	for _, record := range records {
		if p := record.Publish; p != nil {
			if p.Shard {
				hub.publishShard(p.Channel, p.Message)
			} else {
				hub.publish(p.Channel, p.Message)
			}
			continue
		}
		rest = append(rest, record)
	}
	return rest
}
