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
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/hotstuff"
)

// sendNextRoundChange sends the ROUND CHANGE message with current round + 1 发送当前回合+1的回合更改消息
func (c *core) sendNextRoundChange() {
	fmt.Println("----roundchange.go---30------sendNextRoundChange-------------------")
	cv := c.currentView()
	c.sendRoundChange(new(big.Int).Add(cv.Round, common.Big1))
}

// sendRoundChange sends the ROUND CHANGE message with the given round //sendRoundChange使用给定的回合发送回合更改消息
func (c *core) sendRoundChange(round *big.Int) {
	fmt.Println("--------roundchange.go----------37--sendRoundChange--------------------------")
	logger := c.logger.New("state", c.state)

	cv := c.currentView()
	if cv.Round.Cmp(round) >= 0 {
		logger.Error("Cannot send out the round change", "current round", cv.Round, "target round", round)
		return
	}

	// Reset ROUND CHANGE timeout timer with new round number 使用新的轮号重置轮更改超时计时器
	fmt.Println(" 使用新的轮号重置轮更改超时计时器")
	c.catchUpRound(&hotstuff.View{
		// The round number we'd like to transfer to./我们要转到的整数。
		Round: new(big.Int).Set(round),
		// TODO: Need to check if (height - 1) is right //TODO:需要检查（高度-1）是否正确
		// Height: new(big.Int).Set(cv.Height), /高度：新建（大整数）。设置（等高线高度），
		Height: new(big.Int).Sub(cv.Height, common.Big1),
	})

	// Now we have the new round number and block height number //现在我们有了新的轮数和方块高度数
	cv = c.currentView()

	rc := &hotstuff.Subject{
		View:   cv,
		Digest: common.Hash{},
	}

	payload, err := Encode(rc)
	if err != nil {
		logger.Error("Failed to encode ROUND CHANGE", "rc", rc, "err", err)
		return
	}

	c.broadcast(&message{
		Code: msgRoundChange,
		Msg:  payload,
	}, round)
}

// Suppose only the next speaker will receive the ROUND CHANGE message 假设只有下一位演讲者将收到“轮换”消息
func (c *core) handleRoundChange(msg *message, src hotstuff.Validator) error {
	fmt.Println("      roundchange.go 77                  假设只有下一位演讲者将收到“轮换”消息             ")
	logger := c.logger.New("state", c.state, "from", src.Address().Hex())

	// Decode ROUND CHANGE message //解码循环更改消息
	var rc *hotstuff.Subject
	if err := msg.Decode(&rc); err != nil {
		logger.Error("Failed to decode ROUND CHANGE", "err", err)
		return errInvalidMessage
	}

	// This make sure the view should be identical /这样可以确保视图应该相同
	if err := c.checkMessage(msgRoundChange, rc.View); err != nil {
		return err
	}

	cv := c.currentView()
	roundView := rc.View

	// Add the ROUND CHANGE message to its message set and return how many/将ROUND CHANGE消息添加到其消息集中，并返回多少消息
	// messages we've got with the same round number and sequence number.//我们收到的消息具有相同的轮号和序列号。
	num, err := c.roundChangeSet.Add(roundView.Round, msg)
	if err != nil {
		logger.Warn("Failed to add round change message", "from", src, "msg", msg, "err", err)
		return err
	}

	if num == c.HotStuffSize() && (c.waitingForRoundChange || cv.Round.Cmp(roundView.Round) < 0) {
		// We've received n-(n-1)/3 ROUND CHANGE messages, start a new round immediately./我们已收到n-（n-1）/3轮更改消息，请立即开始新一轮。
		fmt.Println("   roundchange.go   105      /我们已收到n-（n-1）/3轮更改消息，请立即开始新一轮。")
		c.startNewRound(roundView.Round)
		return nil
	} else if cv.Round.Cmp(roundView.Round) < 0 {
		// Only gossip the message with current round to other validators.仅将当前回合的消息八卦给其他验证程序。4
		fmt.Println("   roundchange.go   110     仅将当前回合的消息八卦给其他验证程序   ")
		return errIgnored
	}
	return nil
}

// ----------------------------------------------------------------------------

func newRoundChangeSet(valSet hotstuff.ValidatorSet) *roundChangeSet {
	return &roundChangeSet{
		validatorSet: valSet,
		roundChanges: make(map[uint64]*messageSet),
		mu:           new(sync.Mutex),
	}
}

type roundChangeSet struct {
	validatorSet hotstuff.ValidatorSet
	roundChanges map[uint64]*messageSet
	mu           *sync.Mutex
}

// Add adds the round and message into round change set 添加将回合和消息添加到回合更改集中
func (rcs *roundChangeSet) Add(r *big.Int, msg *message) (int, error) {
	fmt.Println("         roundchange.go   134        添加将回合和消息添加到回合更改集中     ")
	rcs.mu.Lock()
	defer rcs.mu.Unlock()

	round := r.Uint64()
	if rcs.roundChanges[round] == nil {
		rcs.roundChanges[round] = newMessageSet(rcs.validatorSet)
	}
	err := rcs.roundChanges[round].Add(msg)
	if err != nil {
		return 0, err
	}
	return rcs.roundChanges[round].Size(), nil
}

// Clear deletes the messages with smaller round
func (rcs *roundChangeSet) Clear(round *big.Int) {
	rcs.mu.Lock()
	defer rcs.mu.Unlock()

	for k, rms := range rcs.roundChanges {
		if len(rms.Values()) == 0 || k < round.Uint64() {
			delete(rcs.roundChanges, k)
		}
	}
}

// MaxRound returns the max round which the number of messages is equal or larger than num
func (rcs *roundChangeSet) MaxRound(num int) *big.Int {
	rcs.mu.Lock()
	defer rcs.mu.Unlock()

	var maxRound *big.Int
	for k, rms := range rcs.roundChanges {
		if rms.Size() < num {
			continue
		}
		r := big.NewInt(int64(k))
		if maxRound == nil || maxRound.Cmp(r) < 0 {
			maxRound = r
		}
	}
	return maxRound
}
