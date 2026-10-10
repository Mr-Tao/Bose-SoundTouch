package webtypes

import (
	"fmt"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

// LoadBassCapabilities loads capabilities once per connection and returns a copy.
func (c *DeviceConnection) LoadBassCapabilities() (*models.BassCapabilities, error) {
	c.bassCapabilitiesMu.Lock()
	defer c.bassCapabilitiesMu.Unlock()

	if c.bassCapabilities == nil {
		if c.Client == nil {
			return nil, fmt.Errorf("speaker client unavailable")
		}

		capabilities, err := c.Client.GetBassCapabilities()
		if err != nil {
			return nil, err
		}

		c.bassCapabilities = capabilities
	}

	result := *c.bassCapabilities

	return &result, nil
}
