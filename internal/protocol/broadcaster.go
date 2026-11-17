package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/invisi-lab/invisi-rpc/internal/model"
)

func (d *DeploymentDecoder) decodeBroadcasters(batch *model.Batch, logs []types.Log) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("malformed broadcaster event")
		}
	}()
	sort.Slice(logs, func(i, j int) bool { return logs[i].Index < logs[j].Index })
	for index, log := range logs {
		if index > 0 && logs[index-1].Index == log.Index {
			return errors.New("duplicate broadcaster log index")
		}
		if log.Removed {
			return errors.New("removed broadcaster log")
		}
		if len(log.Topics) == 0 {
			continue
		}
		set := d.broadcaster.Events["BroadcasterSet"]
		availability := d.broadcaster.Events["AvailabilitySet"]
		event := set
		if log.Topics[0] == availability.ID {
			event = availability
		} else if log.Topics[0] != set.ID {
			continue
		}
		if len(log.Topics) != 2 {
			return errors.New("invalid broadcaster topic count")
		}
		operator := common.BytesToAddress(log.Topics[1][:])
		if operator == (common.Address{}) || common.BytesToHash(operator.Bytes()) != log.Topics[1] {
			return errors.New("invalid broadcaster operator")
		}
		values, err := event.Inputs.NonIndexed().Unpack(log.Data)
		if err != nil {
			return err
		}
		canonical, e := event.Inputs.NonIndexed().Pack(values...)
		if e != nil || !bytes.Equal(log.Data, canonical) {
			return fmt.Errorf("noncanonical %s event", event.Name)
		}
		u := model.BroadcasterUpdate{Operator: operator, LogIndex: log.Index}
		if event.Name == "BroadcasterSet" {
			u.Endpoint = values[0].(string)
			u.MetadataURI = values[1].(string)
			u.QuoteSigner = values[2].(common.Address)
			u.FeeRecipient = values[3].(common.Address)
		} else {
			enabled := values[0].(bool)
			u.Enabled = &enabled
		}
		batch.Broadcasters = append(batch.Broadcasters, u)
		raw, err := json.Marshal(u)
		if err != nil {
			return err
		}
		batch.Events = append(batch.Events, model.Event{Block: batch.Block, Name: event.Name, TransactionHash: log.TxHash, LogIndex: log.Index, Data: raw})
	}
	return nil
}
