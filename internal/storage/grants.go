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
		if g.Owner == (common.Address{}) {
			return nil, errors.New("nonzero grant owner required")
		}
		if g.Token == (common.Address{}) {
			// Core permits disabling an unused ID for the zero token only when
			// no fee is charged. Require explicit canonical event fields so a
			// missing token cannot masquerade as a legitimate pre-revocation.
			var revoke struct {
				Token     *common.Address `json:"token"`
				FeeAmount string          `json:"feeAmount"`
			}
			if e := json.Unmarshal(event.Data, &revoke); e != nil {
				return nil, e
			}
			if event.Name != "CreditRevoked" || revoke.Token == nil || revoke.FeeAmount != "0" {
				return nil, errors.New("zero grant token requires fee-free revocation")
			}
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
