package mqtt

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Pure unit tests: no broker required.
// ---------------------------------------------------------------------------

func TestTopic_Builder(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		host  string
		user  string
		parts []string
		want  string
	}{
		{
			name:  "OnlineTopic",
			base:  "homedrive",
			host:  "nas",
			user:  "fix",
			parts: []string{"online"},
			want:  "homedrive/nas/fix/online",
		},
		{
			name:  "EventTopic",
			base:  "homedrive",
			host:  "nas",
			user:  "fix",
			parts: []string{"events", "push.success"},
			want:  "homedrive/nas/fix/events/push.success",
		},
		{
			name:  "StatusTopic",
			base:  "homedrive",
			host:  "myhost",
			user:  "myuser",
			parts: []string{"status"},
			want:  "homedrive/myhost/myuser/status",
		},
		{
			name:  "NoParts",
			base:  "homedrive",
			host:  "nas",
			user:  "fix",
			parts: nil,
			want:  "homedrive/nas/fix",
		},
		{
			name:  "CustomBase",
			base:  "myapp",
			host:  "server1",
			user:  "admin",
			parts: []string{"metrics"},
			want:  "myapp/server1/admin/metrics",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Client{
				cfg:  Config{BaseTopic: tt.base},
				host: tt.host,
				user: tt.user,
			}
			got := c.Topic(tt.parts...)
			if got != tt.want {
				t.Errorf("Topic(%v) = %q, want %q", tt.parts, got, tt.want)
			}
		})
	}
}

func TestWithDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   Config
		want Config
	}{
		{
			name: "AllZeros",
			in:   Config{},
			want: Config{
				QoS:            1,
				KeepAlive:      30 * time.Second,
				ReconnectMax:   5 * time.Minute,
				BaseTopic:      "homedrive",
				ClientIDPrefix: "homedrive",
			},
		},
		{
			name: "CustomValues",
			in: Config{
				QoS:            2,
				KeepAlive:      10 * time.Second,
				ReconnectMax:   1 * time.Minute,
				BaseTopic:      "mybase",
				ClientIDPrefix: "myprefix",
			},
			want: Config{
				QoS:            2,
				KeepAlive:      10 * time.Second,
				ReconnectMax:   1 * time.Minute,
				BaseTopic:      "mybase",
				ClientIDPrefix: "myprefix",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := withDefaults(tt.in)
			if got.QoS != tt.want.QoS {
				t.Errorf("QoS: got %d, want %d", got.QoS, tt.want.QoS)
			}
			if got.KeepAlive != tt.want.KeepAlive {
				t.Errorf("KeepAlive: got %v, want %v", got.KeepAlive, tt.want.KeepAlive)
			}
			if got.ReconnectMax != tt.want.ReconnectMax {
				t.Errorf("ReconnectMax: got %v, want %v", got.ReconnectMax, tt.want.ReconnectMax)
			}
			if got.BaseTopic != tt.want.BaseTopic {
				t.Errorf("BaseTopic: got %q, want %q", got.BaseTopic, tt.want.BaseTopic)
			}
			if got.ClientIDPrefix != tt.want.ClientIDPrefix {
				t.Errorf("ClientIDPrefix: got %q, want %q",
					got.ClientIDPrefix, tt.want.ClientIDPrefix)
			}
		})
	}
}

func TestClientID(t *testing.T) {
	got := clientID("homedrive", "nas", "fix")
	want := "homedrive_nas_fix"
	if got != want {
		t.Errorf("clientID() = %q, want %q", got, want)
	}
}

func TestToBytes(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    string
		wantErr bool
	}{
		{
			name:  "ByteSlice",
			input: []byte("hello"),
			want:  "hello",
		},
		{
			name:  "String",
			input: "world",
			want:  "world",
		},
		{
			name:  "Struct",
			input: struct{ X int }{X: 42},
			want:  `{"X":42}`,
		},
		{
			name:    "Channel",
			input:   make(chan int),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toBytes(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("toBytes() = %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestPublisherInterface(t *testing.T) {
	// Compile-time check that *Client implements Publisher.
	var _ Publisher = (*Client)(nil)
}
