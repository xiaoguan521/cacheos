package paramcat

import "testing"

func TestEqual(t *testing.T) {
	for _, c := range [][3]string{
		{"maxmemory", "1073741824", "1gb"},
		{"notify-keyspace-events", "xE", "Ex"},
		{"notify-keyspace-events", "AKE", "KEg$lshzxetd"},
		{"client-output-buffer-limit", "normal 0 0 0 replica 268435456 67108864 60 pubsub 33554432 8388608 60", "pubsub 32mb 8mb 60"},
		{"client-output-buffer-limit", "normal 0 0 0 slave 268435456 67108864 60 pubsub 33554432 8388608 60", "slave 256mb 64mb 60"},
		{"appendonly", "yes", "yes"},
	} {
		if !Equal(c[0], c[1], c[2]) {
			t.Errorf("Equal(%q, %q, %q) = false", c[0], c[1], c[2])
		}
	}
	for _, c := range [][3]string{
		{"maxmemory", "1073741824", "1g"},
		{"notify-keyspace-events", "xE", "Kx"},
		{"client-output-buffer-limit", "normal 0 0 0 pubsub 33554432 8388608 60", "pubsub 64mb 8mb 60"},
		{"hz", "10", "20"},
	} {
		if Equal(c[0], c[1], c[2]) {
			t.Errorf("Equal(%q, %q, %q) = true", c[0], c[1], c[2])
		}
	}
}
