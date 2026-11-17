package storage

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

type grantEvent struct {
	Owner           common.Address `json:"owner"`
	Token           common.Address `json:"token"`
	RevocationShard json.Number    `json:"revocationShard"`
	RevocationSlot  uint16         `json:"revocationSlot"`
	Shard           json.Number    `json:"shard"`
	Slot            uint16         `json:"slot"`
}

func grants(b model.Batch) ([]struct {
	Event model.Event
	Grant grantEvent
}, error) {
	out := []struct {
		Event model.Event
		Grant grantEvent
	}{}
	for _, event := range b.Events {
		if event.Name != "CreditCreated" && event.Name != "CreditRevoked" {
			continue
		}
		var g grantEvent
		if e := json.Unmarshal(event.Data, &g); e != nil {
			return nil, e
		}
		if event.Name == "CreditCreated" {
			g.Shard = g.RevocationShard
			g.Slot = g.RevocationSlot
		}
		if sh, e := strconv.ParseUint(string(g.Shard), 10, 64); e != nil || sh == 0 {
			return nil, fmt.Errorf("invalid zero grant shard")
		}
		out = append(out, struct {
			Event model.Event
			Grant grantEvent
		}{event, g})
	}
	return out, nil
}
