// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	// "bytes"
	"fmt"
	"math"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/hotstuff"
	"github.com/ethereum/go-ethereum/params"

	// "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	metrics "github.com/ethereum/go-ethereum/metrics"
	"gopkg.in/karalabe/cookiejar.v2/collections/prque"
)

// New creates an HotStuff consensus core
func New(backend hotstuff.Backend, config *hotstuff.Config) CoreEngine {
	r := metrics.NewRegistry()
	c := &core{
		config: config,
		// state:              StateAcceptRequest,
		state:                           StateSendPub,
		address:                         backend.Address(),
		handlerWg:                       new(sync.WaitGroup),
		logger:                          log.New("address", backend.Address()),
		backend:                         backend,
		backlogs:                        make(map[common.Address]*prque.Prque),
		backlogsMu:                      new(sync.Mutex),
		pendingRequests:                 prque.New(),
		pendingRequestsMu:               new(sync.Mutex),
		pendingRequestsUnconfirmedQueue: hotstuff.NewQueue(int(params.MinimumUnconfirmed)),
		consensusTimestamp:              time.Time{},
		roundMeter:                      metrics.NewMeter(),
		blockheightMeter:                metrics.NewMeter(),
		consensusTimer:                  metrics.NewTimer(),
	}

	r.Register("consensus/hotstuff/core/round", c.roundMeter)
	r.Register("consensus/hotstuff/core/blockheight", c.blockheightMeter)
	r.Register("consensus/hotstuff/core/consensus", c.consensusTimer)

	c.validateFn = c.checkValidatorSignature
	return c
}

// ----------------------------------------------------------------------------

type core struct {
	config  *hotstuff.Config
	address common.Address
	state   State
	logger  log.Logger

	backend             hotstuff.Backend
	events              *event.TypeMuxSubscription
	finalCommittedSub   *event.TypeMuxSubscription
	timeoutSub          *event.TypeMuxSubscription
	futureAnnounceTimer *time.Timer

	valSet                hotstuff.ValidatorSet
	waitingForRoundChange bool
	validateFn            func([]byte, []byte) (common.Address, error)

	hasAggPub bool

	backlogs   map[common.Address]*prque.Prque
	backlogsMu *sync.Mutex

	current   *roundState
	handlerWg *sync.WaitGroup

	roundChangeSet   *roundChangeSet
	roundChangeTimer *time.Timer

	pendingRequests                 *prque.Prque
	pendingRequestsMu               *sync.Mutex
	pendingRequestsUnconfirmedQueue *hotstuff.Queue

	consensusTimestamp time.Time
	// the meter to record the round change rate
	roundMeter metrics.Meter
	// the meter to record the block height update rate
	blockheightMeter metrics.Meter
	// the timer to record consensus duration (from accepting a preprepare to final committed stage)
	consensusTimer metrics.Timer
}

func (c *core) finalizeMessage(msg *message) ([]byte, error) {
	var err error

	// Add local address and aggregated-oriented pub and sign //添加本地地址和面向聚合的发布和签名
	msg.Address = common.Address{}
	msg.AggPub = []byte{}
	msg.AggSign = []byte{}

	// Assign the AggPub, AggSign, and Mask if it's a RESPONSE message and proposal is not nil
	//如果是响应消息且建议不是nil，则分配AggPub、AggSign和Mask
	if (msg.Code == msgResponse || msg.Code == msgRoundChange) && c.current.Proposal() != nil {
		signedData, err := msg.PayloadNoAddrNoAggNoSig()
		if err != nil {
			return nil, err
		}
		msg.AggPub, msg.AggSign, err = c.backend.AggregatedSignedFromSingle(signedData)
		if err != nil {
			return nil, err
		}
	}
	fmt.Println("       core.go      130                                       最终消息            ")
	// Add sender address 最终消息
	msg.Address = c.Address()

	// Sign message//签名信息
	data, err := msg.PayloadNoSig()
	if err != nil {
		return nil, err
	}
	msg.Signature, err = c.backend.Sign(data)
	if err != nil {
		return nil, err
	}

	// Convert to payload 转换为有效载荷
	payload, err := msg.Payload()
	if err != nil {
		return nil, err
	}

	return payload, nil
}

func (c *core) broadcast(msg *message, round *big.Int) {
	logger := c.logger.New("state", c.state)

	payload, err := c.finalizeMessage(msg)
	if err != nil {
		logger.Error("Failed to finalize message", "msg", msg, "err", err)
		return
	}

	if msg.Code == msgResponse && c.current.Proposal() != nil {
		// Unicast payload to the current speaker 单播有效载荷到当前扬声器
		if err = c.backend.Unicast(c.valSet, payload); err != nil {
			logger.Error("Failed to unicast message", "msg", msg, "err", err)
			return
		}
	} else if msg.Code == msgRoundChange && c.current.Proposal() != nil {
		// Calculate the new speaker 计算新的扬声器
		_, lastSpeaker := c.backend.LastProposal()
		proposedNewSet := c.valSet.Copy()
		proposedNewSet.CalcSpeaker(lastSpeaker, round.Uint64())
		if !proposedNewSet.IsSpeaker(c.Address()) {
			// Unicast payload to the proposed speaker //向提议的说话人发送单播有效载荷
			fmt.Println("        core.go        175          //向提议的说话人发送单播有效载荷")
			if err = c.backend.Unicast(proposedNewSet, payload); err != nil {
				logger.Error("Failed to unicast message", "msg", msg, "err", err)
				return
			}
		} else {
			logger.Trace("Local is the next speaker", "msg", msg)
			return
		}
	} else {
		// Broadcast payload/广播有效载荷
		fmt.Println("     core.go   186       Broadcast payload /广播有效载荷  ")
		if err = c.backend.Broadcast(c.valSet, payload); err != nil {
			logger.Error("Failed to broadcast message", "msg", msg, "err", err)
			return
		}
	}

}

func (c *core) currentView() *hotstuff.View {
	fmt.Println("  core--core.go     196       currentView      ")
	return &hotstuff.View{
		Height: new(big.Int).Set(c.current.Height()),
		Round:  new(big.Int).Set(c.current.Round()),
	}
}

func (c *core) IsSpeaker() bool {
	v := c.valSet
	if v == nil {
		return false
	}
	return v.IsSpeaker(c.backend.Address())
}

func (c *core) IsCurrentProposal(blockHash common.Hash) bool {
	return c.current != nil && c.current.pendingRequest != nil && c.current.pendingRequest.Proposal.Hash() == blockHash
}

func (c *core) Address() common.Address {
	return c.address
}

// func (c *core) SetAddressAndLogger(addr common.Address) {
// 	c.address = addr
// 	c.logger = log.New("address", c.backend.GetAddress())
// }

func (c *core) commit(roundChange bool, round *big.Int) {
	c.setState(StateResponsed)

	collectionPub := make(map[common.Address][]byte)
	collectionSig := make(map[common.Address][]byte)
	fmt.Println("               core.go             229                  ")
	if !roundChange {
		proposal := c.current.Proposal()
		if proposal != nil {
			for _, msg := range c.current.Responses.Values() {
				if msg.Code == msgResponse {

					// Notes: msg.AggPub and msg.AggSign are assigned by calling core.go/finalizeMessage at the delegators' sides

					collectionPub[msg.Address], collectionSig[msg.Address] = msg.AggPub, msg.AggSign
				}
			}
			if err := c.backend.Commit(proposal, c.valSet, collectionPub, collectionSig); err != nil {
				c.sendNextRoundChange()
				return
			}
		}
	} else {
		// Round Change
		fmt.Println("                 core.go  248              轮换             ")
		if !c.pendingRequestsUnconfirmedQueue.Empty() {
			proposal, err := c.pendingRequestsUnconfirmedQueue.GetFirst()
			if err != nil {
				c.sendNextRoundChange()
				return
			}
			for _, msg := range c.roundChangeSet.roundChanges[round.Uint64()].Values() {
				if msg.Code == msgRoundChange {
					collectionPub[msg.Address], collectionSig[msg.Address] = msg.AggPub, msg.AggSign
				}
			}
			fmt.Println("core.go 254           c.backend.Commit    ")
			if err := c.backend.Commit(proposal.(hotstuff.Proposal), c.valSet, collectionPub, collectionSig); err != nil {
				c.sendNextRoundChange()
				return
			}
			fmt.Println("core.go 259          c.backend.Commit    ")

		}

	}

}

// startNewRound starts a new round. if round equals to 0, it means to starts a new block height
//StartNew开始新一轮。如果round等于0，则表示开始新的块高度
func (c *core) startNewRound(round *big.Int) {
	var logger log.Logger
	if c.current == nil {
		logger = c.logger.New("old_round", -1, "old_height", 0)
	} else {
		logger = c.logger.New("old_round", c.current.Round(), "old_height", c.current.Height())
	}

	roundChange := false
	// Try to get last proposal 争取得到最后的建议
	fmt.Println("                   core.go       285                  争取得到最后的建议")
	lastProposal, lastSpeaker := c.backend.LastProposal()
	if c.current == nil {
		logger.Trace("Start to the initial round")
	} else if lastProposal.Number().Cmp(c.current.Height()) >= 0 {
		diff := new(big.Int).Sub(lastProposal.Number(), c.current.Height())
		c.blockheightMeter.Mark(new(big.Int).Add(diff, common.Big1).Int64())

		if !c.consensusTimestamp.IsZero() {
			c.consensusTimer.UpdateSince(c.consensusTimestamp)
			c.consensusTimestamp = time.Time{}
		}
		logger.Trace("Catch up latest proposal", "number", lastProposal.Number().Uint64(), "hash", lastProposal.Hash())
		// BLS:
		// The Proposal in c.current has to be 1 block higher than lastProposal. In hotstuff, however, c.current should
		////c.current中的提案必须比lastProposal高1个街区。然而，在hotstuff中，c.current应该
		// replace the lastProposal.Number()-1 because of the three-phase view change protocol.
		//替换lastProposal.Number（）-1，因为存在三相视图更改协议。
		// /BLS
	} else if lastProposal.Number().Cmp(big.NewInt(c.current.Height().Int64()-1)) == 0 {
		if round.Cmp(common.Big0) == 0 {
			// same height and round, don't need to start new round
			return
		} else if round.Cmp(c.current.Round()) < 0 {
			logger.Warn("New round should not be smaller than current round", "height", lastProposal.Number().Int64(), "new_round", round, "old_round", c.current.Round())
			return
		}
		roundChange = true
	} else {
		logger.Warn("New height should be larger than current height", "new_height", lastProposal.Number().Int64())
		return
	}

	var newView *hotstuff.View
	if roundChange {
		newView = &hotstuff.View{
			// TODO: Need to check if (height - 1) is right      需要检查（高度-1）是否正确
			// Height: new(big.Int).Set(c.current.Height()), 高度：新建（big.Int）.Set（c.current.Height（）），
			Height: new(big.Int).Sub(c.current.Height(), common.Big1),
			Round:  new(big.Int).Set(round),
		}
	} else {
		newView = &hotstuff.View{
			Height: new(big.Int).Add(lastProposal.Number(), common.Big1),
			Round:  new(big.Int),
		}
		c.valSet = c.backend.Validators(lastProposal)
	}

	// Update logger
	logger = logger.New("old_speaker", c.valSet.GetSpeaker())
	// Clear invalid ROUND CHANGE messages
	c.roundChangeSet = newRoundChangeSet(c.valSet)
	// New snapshot for new round
	c.updateRoundState(newView, c.valSet, roundChange)
	// Calculate new proposer and update the valSet
	c.valSet.CalcSpeaker(lastSpeaker, newView.Round.Uint64())
	c.waitingForRoundChange = false

	if !c.hasAggPub {
		c.setState(StateSendPub)
	} else {
		c.setState(StateAcceptRequest)
	}

	if roundChange && c.IsSpeaker() && c.current != nil && c.hasAggPub && c.state == StateAcceptRequest {
		if c.current.pendingRequest != nil && !c.pendingRequestsUnconfirmedQueue.Empty() {
			// Hotstuff view change replacing pendingRequest, commit directly with aggsig of roundchange
			c.commit(true, round)
		}
	}
	c.newRoundChangeTimer()

	logger.Debug("New round", "new_round", newView.Round, "new_height", newView.Height, "old_speaker", c.valSet.GetSpeaker(), "valSet", c.valSet.List(), "size", c.valSet.Size(), "IsSpeaker", c.IsSpeaker())
}

func (c *core) catchUpRound(view *hotstuff.View) {
	logger := c.logger.New("old_round", c.current.Round(), "old_height", c.current.Height(), "old_speaker", c.valSet.GetSpeaker())

	if view.Round.Cmp(c.current.Round()) > 0 {
		c.roundMeter.Mark(new(big.Int).Sub(view.Round, c.current.Round()).Int64())
	}
	c.waitingForRoundChange = true

	c.updateRoundState(view, c.valSet, true)
	c.roundChangeSet.Clear(view.Round)
	c.newRoundChangeTimer()

	logger.Trace("Catch up round", "new_round", view.Round, "new_height", view.Height, "new_speaker", c.valSet)
}

// updateRoundState updates round state 更新循环状态
func (c *core) updateRoundState(view *hotstuff.View, validatorSet hotstuff.ValidatorSet, roundChange bool) {
	if roundChange && c.current != nil {
		// Hotstuff view change replacing pendingRequest /Hotstuff视图更改替换挂起请求
		if !c.pendingRequestsUnconfirmedQueue.Empty() {
			proposal, err := c.pendingRequestsUnconfirmedQueue.GetFirst()
			fmt.Println("///////////proposal///////////", proposal)
			fmt.Println("///////////err///////////", err)
			if err != nil {
				c.logger.Trace("Invalid unconfirmed queue")
				return
			} else {
				fmt.Println("*****************r := &hotstuff.Request****************")
				// p1 := new(hotstuff.Proposal)
				// proposal = &p1
				r := &hotstuff.Request{

					Proposal: proposal.(hotstuff.Proposal),
				}

				c.current = newRoundState(view, validatorSet, nil, r, c.backend.HasBadProposal)

			}
		}
	} else {
		fmt.Println("-..--------core.go    401---------------")
		c.current = newRoundState(view, validatorSet, nil, nil, c.backend.HasBadProposal)
	}
}

func (c *core) setState(state State) {
	if c.state != state {
		c.state = state
	}
	if state == StateAcceptRequest {
		c.processPendingRequests()
	}
	c.processBacklog()
}

func (c *core) stopFutureAnnounceTimer() {
	if c.futureAnnounceTimer != nil {
		c.futureAnnounceTimer.Stop()
	}
}

func (c *core) stopTimer() {
	c.stopFutureAnnounceTimer()
	if c.roundChangeTimer != nil {
		c.roundChangeTimer.Stop()
	}
}

func (c *core) newRoundChangeTimer() {
	c.stopTimer()

	// set timeout based on the round number
	timeout := time.Duration(c.config.RequestTimeout) * time.Millisecond
	round := c.current.Round().Uint64()
	if round > 0 {
		timeout += time.Duration(math.Pow(2, float64(round))) * time.Second
	}
	c.roundChangeTimer = time.AfterFunc(timeout, func() {
		c.sendEvent(timeoutEvent{})
	})
}

func (c *core) checkValidatorSignature(data []byte, sig []byte) (common.Address, error) {
	return hotstuff.CheckValidatorSignature(c.valSet, data, sig)
}

func (c *core) addAggPub(address common.Address, pubByte []byte) (int, error) {
	return c.backend.AddAggPub(c.valSet, address, pubByte)
}

func (c *core) countAggPub() int {
	return c.backend.CountAggPub()
}

func (c *core) HotStuffSize() int {
	c.logger.Trace("Confirmation Formula used N-(N-1)/3")
	return int((c.valSet.Size() - (c.valSet.Size()-1)/3))
}

// PrepareCommittedSeal returns a committed seal for the given hash 返回给定哈希的已提交密封
func (c *core) CurrentRoundstate() *roundState {
	if c.current != nil {
		return c.current
	}
	return nil
}
