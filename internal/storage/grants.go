package storage

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/invisi-labs/invisi-rpc/internal/model"
)

type grantEvent struct {
	Owner   common.Address `json:"owner"`
	Token   common.Address `json:"token"`
	GrantID string         `json:"grantId"`
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
		if e := model.ValidateGrantID(g.GrantID); e != nil {
			return nil, errors.New("unsupported legacy or invalid grant history: canonical positive uint128 grantId required; rebuild from current Core deployment")
		}
		if g.Owner == (common.Address{}) || g.Token == (common.Address{}) {
			return nil, errors.New("nonzero grant owner and token required")
		}
		out = append(out, struct {
			Event model.Event
			Grant grantEvent
		}{event, g})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Event.LogIndex < out[j].Event.LogIndex })
	return out, nil
}
func grantTransition(previous uint8, kind string) (uint8, error) {
	if previous > 2 {
		return 0, errors.New("invalid persisted credit status")
	}
	if kind == "CreditCreated" && previous == 0 {
		return 1, nil
	}
	if kind == "CreditRevoked" && previous != 2 {
		return 2, nil
	}
	return 0, errors.New("invalid credit status transition: grant IDs cannot be reused or revoked twice")
}
