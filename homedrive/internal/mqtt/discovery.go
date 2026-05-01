package mqtt

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// HAComponent represents a Home Assistant MQTT component type.
type HAComponent string

const (
	// HASensor is a Home Assistant sensor component.
	HASensor HAComponent = "sensor"
	// HABinarySensor is a Home Assistant binary sensor component.
	HABinarySensor HAComponent = "binary_sensor"
)

// EntityDef defines a single Home Assistant entity for MQTT Discovery.
type EntityDef struct {
	Key          string      // e.g. "status", "last_push"
	Name         string      // human-readable name for HA UI
	Component    HAComponent // sensor or binary_sensor
	DeviceClass  string      // HA device_class (optional)
	Icon         string      // mdi icon (optional)
	Unit         string      // unit_of_measurement (optional)
	StateTopic   string      // relative to base topic (e.g. "status")
	ValueTmpl    string      // HA value_template (optional)
	PayloadOn    string      // for binary_sensor only
	PayloadOff   string      // for binary_sensor only
	ExpireAfter  int         // seconds before HA marks unavailable (optional)
}

// DeviceBlock is the shared device information linking all entities in HA.
type DeviceBlock struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	SWVersion    string   `json:"sw_version"`
	Model        string   `json:"model"`
	Manufacturer string   `json:"manufacturer"`
}

// DiscoveryConfig is the JSON payload for an HA MQTT Discovery message.
type DiscoveryConfig struct {
	Name              string      `json:"name"`
	UniqueID          string      `json:"unique_id"`
	StateTopic        string      `json:"state_topic"`
	Device            DeviceBlock `json:"device"`
	DeviceClass       string      `json:"device_class,omitempty"`
	Icon              string      `json:"icon,omitempty"`
	UnitOfMeasurement string      `json:"unit_of_measurement,omitempty"`
	ValueTemplate     string      `json:"value_template,omitempty"`
	PayloadOn         string      `json:"payload_on,omitempty"`
	PayloadOff        string      `json:"payload_off,omitempty"`
	ExpireAfter       int         `json:"expire_after,omitempty"`
}

// Entities returns the 10 entity definitions from PLAN.md section 9.1.
func Entities() []EntityDef {
	return []EntityDef{
		{
			Key:        "status",
			Name:       "Status",
			Component:  HASensor,
			Icon:       "mdi:sync",
			StateTopic: "status",
		},
		{
			Key:         "last_push",
			Name:        "Last Push",
			Component:   HASensor,
			DeviceClass: "timestamp",
			Icon:        "mdi:cloud-upload",
			StateTopic:  "last_push",
		},
		{
			Key:         "last_pull",
			Name:        "Last Pull",
			Component:   HASensor,
			DeviceClass: "timestamp",
			Icon:        "mdi:cloud-download",
			StateTopic:  "last_pull",
		},
		{
			Key:        "pending_up",
			Name:       "Pending Uploads",
			Component:  HASensor,
			Icon:       "mdi:upload",
			StateTopic: "queue/pending_up",
		},
		{
			Key:        "pending_down",
			Name:       "Pending Downloads",
			Component:  HASensor,
			Icon:       "mdi:download",
			StateTopic: "queue/pending_down",
		},
		{
			Key:        "conflicts_24h",
			Name:       "Conflicts (24h)",
			Component:  HASensor,
			Icon:       "mdi:alert-circle",
			StateTopic: "conflicts_24h",
		},
		{
			Key:        "bytes_up_24h",
			Name:       "Bytes Uploaded (24h)",
			Component:  HASensor,
			Icon:       "mdi:upload-network",
			Unit:       "B",
			StateTopic: "bytes_up_24h",
		},
		{
			Key:        "bytes_down_24h",
			Name:       "Bytes Downloaded (24h)",
			Component:  HASensor,
			Icon:       "mdi:download-network",
			Unit:       "B",
			StateTopic: "bytes_down_24h",
		},
		{
			Key:         "quota_used_pct",
			Name:        "Drive Quota Used",
			Component:   HASensor,
			DeviceClass: "data_size",
			Icon:        "mdi:google-drive",
			Unit:        "%",
			StateTopic:  "quota_used_pct",
		},
		{
			Key:         "online",
			Name:        "Online",
			Component:   HABinarySensor,
			DeviceClass: "connectivity",
			StateTopic:  "online",
			PayloadOn:   "online",
			PayloadOff:  "offline",
		},
	}
}

// buildDeviceBlock returns the shared device block for all entities.
func buildDeviceBlock(host, user, swVersion string) DeviceBlock {
	return DeviceBlock{
		Identifiers:  []string{fmt.Sprintf("homedrive_%s_%s", host, user)},
		Name:         fmt.Sprintf("homedrive (%s@%s)", user, host),
		SWVersion:    swVersion,
		Model:        "homedrive",
		Manufacturer: "asnowfix/home-automation",
	}
}

// discoveryTopic builds the HA discovery config topic for an entity.
// Format: <prefix>/<component>/homedrive_<host>_<user>_<key>/config
func discoveryTopic(prefix string, host, user string, entity EntityDef) string {
	objectID := fmt.Sprintf("homedrive_%s_%s_%s", host, user, entity.Key)
	return strings.Join([]string{prefix, string(entity.Component), objectID, "config"}, "/")
}

// buildDiscoveryPayload creates the JSON bytes for a single entity's
// HA Discovery config message.
func buildDiscoveryPayload(entity EntityDef, device DeviceBlock, stateTopic string) ([]byte, error) {
	cfg := DiscoveryConfig{
		Name:       entity.Name,
		UniqueID:   fmt.Sprintf("homedrive_%s_%s", device.Identifiers[0], entity.Key),
		StateTopic: stateTopic,
		Device:     device,
	}
	if entity.DeviceClass != "" {
		cfg.DeviceClass = entity.DeviceClass
	}
	if entity.Icon != "" {
		cfg.Icon = entity.Icon
	}
	if entity.Unit != "" {
		cfg.UnitOfMeasurement = entity.Unit
	}
	if entity.ValueTmpl != "" {
		cfg.ValueTemplate = entity.ValueTmpl
	}
	if entity.PayloadOn != "" {
		cfg.PayloadOn = entity.PayloadOn
	}
	if entity.PayloadOff != "" {
		cfg.PayloadOff = entity.PayloadOff
	}
	if entity.ExpireAfter > 0 {
		cfg.ExpireAfter = entity.ExpireAfter
	}
	return json.Marshal(cfg)
}

// PublishDiscovery publishes HA MQTT Discovery config messages for all
// 10 entities. Each message is published with retain=true so HA discovers
// them even if it restarts after homedrive. Called at startup and on /reload.
func PublishDiscovery(pub Publisher, cfg Config, host, user, swVersion string, log *slog.Logger) error {
	device := buildDeviceBlock(host, user, swVersion)
	entities := Entities()
	prefix := cfg.HADiscoveryPrefix
	if prefix == "" {
		prefix = "homeassistant"
	}

	var firstErr error
	for _, entity := range entities {
		topic := discoveryTopic(prefix, host, user, entity)
		stateTopic := pub.Topic(entity.StateTopic)
		payload, err := buildDiscoveryPayload(entity, device, stateTopic)
		if err != nil {
			log.Error("mqtt discovery payload build failed",
				"entity", entity.Key, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("discovery payload for %s: %w", entity.Key, err)
			}
			continue
		}
		if err := pub.Publish(topic, cfg.QoS, true, payload); err != nil {
			log.Error("mqtt discovery publish failed",
				"entity", entity.Key, "topic", topic, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("discovery publish for %s: %w", entity.Key, err)
			}
		} else {
			log.Debug("mqtt discovery published",
				"entity", entity.Key, "topic", topic)
		}
	}
	return firstErr
}
