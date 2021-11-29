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
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/hotstuff"
)

// Start implements core.Engine.Start
func (c *core) Start() error {
	// Start a new round from last height + 1
	c.startNewRound(common.Big0)

	// Tests will handle events itself, so we have to make subscribeEvents()
	// be able to call in test.
	c.subscribeEvents()
	fmt.Println("--------------(c *core) Start() ------------------")
	go c.handleEvents()

	return nil
}

// Stop implements core.Engine.Stop
func (c *core) Stop() error {
	c.stopTimer()
	c.unsubscribeEvents()

	// Make sure the handler goroutine exits
	c.handlerWg.Wait()
	return nil
}

// ----------------------------------------------------------------------------

// Subscribe both internal and external events
func (c *core) subscribeEvents() {
	c.events = c.backend.EventMux().Subscribe(
		// external events
		hotstuff.RequestEvent{},
		hotstuff.MessageEvent{},
		hotstuff.SendingPubEvent{},
		// internal events
		backlogEvent{},
	)
	c.timeoutSub = c.backend.EventMux().Subscribe(
		timeoutEvent{},
	)
	c.finalCommittedSub = c.backend.EventMux().Subscribe(
		hotstuff.FinalCommittedEvent{},
	)
}

// Unsubscribe all events
func (c *core) unsubscribeEvents() {
	c.logger.Trace("Closing event channels")
	if c.events != nil {
		c.events.Unsubscribe()
	}
	if c.timeoutSub != nil {
		c.timeoutSub.Unsubscribe()
	}
	if c.finalCommittedSub != nil {
		c.finalCommittedSub.Unsubscribe()
	}
}

func (c *core) handleEvents() {
	// Clear state
	defer func() {
		c.current = nil
		c.handlerWg.Done()
	}()

	c.handlerWg.Add(1)
	for {
		select {
		case event, ok := <-c.events.Chan():
			if !ok {
				return
			}
			// A real event arrived, process interesting content    一个真实的事件到来，处理有趣的内容
			switch ev := event.Data.(type) {
			case hotstuff.SendingPubEvent:
				c.sendPub(ev.Payload)
			case hotstuff.RequestEvent:
				r := &hotstuff.Request{
					Proposal: ev.Proposal,
				}
				err := c.handleRequest(r)
				if err == errFutureMessage {
					c.storeRequestMsg(r)
				}
			case hotstuff.MessageEvent:
				if err := c.handleMsg(ev.Payload); err == nil {
					// This just forwards what we just received...but we use a recentMessage to skip those already have
					///这只是转发我们刚刚收到的内容…但我们使用recentMessage跳过已经收到的内容
					c.backend.Gossip(c.valSet, ev.Payload)
				}
			case backlogEvent:
				// No need to check signature for internal messages /无需检查内部消息的签名
				if err := c.handleCheckedMsg(ev.msg, ev.src); err == nil {
					p, err := ev.msg.Payload()
					if err != nil {
						c.logger.Warn("Get message payload failed", "err", err)
						continue
					}
					c.backend.Gossip(c.valSet, p)
				}
			}
		case _, ok := <-c.timeoutSub.Chan():
			if !ok {
				return
			}
			fmt.Println("-------------c.handleTimeoutMsg()---------------")
			c.handleTimeoutMsg()
		case event, ok := <-c.finalCommittedSub.Chan():
			if !ok {
				return
			}
			switch event.Data.(type) {
			case hotstuff.FinalCommittedEvent:
				c.handleFinalCommitted()
			}
		}
	}
}

// sendEvent sends events to mux
func (c *core) sendEvent(ev interface{}) {
	c.backend.EventMux().Post(ev)
}

func (c *core) handleMsg(payload []byte) error {
	logger := c.logger.New()

	// Decode message and check its signature
	msg := new(message)
	if err := msg.FromPayload(payload, c.validateFn); err != nil {
		fmt.Println("		msg.FromPayload                        payload)", payload)
		logger.Error("Failed to decode message from payload", "err", err)
		return err
	}

	// Only accept message if the address is valid
	_, src := c.valSet.GetByAddress(msg.Address)
	if src == nil {
		logger.Error("Invalid address in message", "msg", msg)
		return hotstuff.ErrUnauthorizedAddress
	}

	return c.handleCheckedMsg(msg, src)
}

func (c *core) handleCheckedMsg(msg *message, src hotstuff.Validator) error {
	logger := c.logger.New("address", c.address, "from", src)

	// Store the message if it's a future message
	testBacklog := func(err error) error {
		if err == errFutureMessage {
			c.storeBacklog(msg, src)
		}

		return err
	}

	switch msg.Code {
	case msgSendPub:
		return testBacklog(c.handleSendPub(msg, src))
	case msgAnnounce:
		return testBacklog(c.handleAnnounce(msg, src))
	case msgResponse:
		return testBacklog(c.handleResponse(msg, src))
	case msgRoundChange:
		return testBacklog(c.handleRoundChange(msg, src))
	default:
		logger.Error("Invalid message", "msg", msg)
	}

	return errInvalidMessage
}

func (c *core) handleTimeoutMsg() {
	// If we're not waiting for round change yet, we can try to catch up 如果我们还没有等到轮换，我们可以努力赶上
	// the max round with F+1 round change message. We only need to catch up F+1轮次更改消息的最大轮次。我们只需要赶上
	// if the max round is larger than current round.  如果最大轮数大于当前轮数。
	// TODO: Need to check if this also works for hotstuff //TODO:需要检查这是否也适用于hotstuff

	fmt.Println("-------------handleTimeoutMsg------------------")
	fmt.Println("-------------c.waitingForRoundChange------------------", c.waitingForRoundChange)
	if !c.waitingForRoundChange {
		maxRound := c.roundChangeSet.MaxRound(c.valSet.F() + 1)
		if maxRound != nil && maxRound.Cmp(c.current.Round()) > 0 {
			c.sendRoundChange(maxRound)
			return
		}
	}

	lastProposal, _ := c.backend.LastProposal()
	fmt.Println("-------------lastProposal-----------------")
	if lastProposal != nil && lastProposal.Number().Cmp(c.current.Height()) >= 0 {
		c.logger.Trace("round change timeout, catch up latest height", "number", lastProposal.Number().Uint64())
		c.startNewRound(common.Big0)

	} else {
		c.sendNextRoundChange()
		fmt.Println(".............core-handler.go...223.")
	}
}
