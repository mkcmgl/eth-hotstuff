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
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/hotstuff"
)

// Construct a new message set to accumulate messages for given height/view number.构造一个新的消息集，以累积给定高度/视图编号的消息。
func newMessageSet(valSet hotstuff.ValidatorSet) *messageSet {
	return &messageSet{
		view: &hotstuff.View{
			Round:  new(big.Int),
			Height: new(big.Int),
		},
		messagesMu: new(sync.Mutex),
		messages:   make(map[common.Address]*message),
		valSet:     valSet,
	}
}

// ----------------------------------------------------------------------------

type messageSet struct {
	view       *hotstuff.View
	valSet     hotstuff.ValidatorSet
	messagesMu *sync.Mutex
	messages   map[common.Address]*message
}

func (ms *messageSet) View() *hotstuff.View {
	return ms.view
}

func (ms *messageSet) Add(msg *message) error {
	ms.messagesMu.Lock()
	defer ms.messagesMu.Unlock()

	if err := ms.verify(msg); err != nil {
		return err
	}

	return ms.addVerifiedMessage(msg)
}

func (ms *messageSet) Values() (result []*message) {
	ms.messagesMu.Lock()
	defer ms.messagesMu.Unlock()

	for _, v := range ms.messages {
		result = append(result, v)
	}

	return result
}

func (ms *messageSet) Size() int {
	ms.messagesMu.Lock()
	defer ms.messagesMu.Unlock()
	return len(ms.messages)
}

func (ms *messageSet) Get(addr common.Address) *message {
	ms.messagesMu.Lock()
	defer ms.messagesMu.Unlock()
	return ms.messages[addr]
}

// ----------------------------------------------------------------------------

func (ms *messageSet) verify(msg *message) error {
	// verify if the message comes from one of the validators 验证消息是否来自其中一个验证器
	if _, v := ms.valSet.GetByAddress(msg.Address); v == nil {
		return hotstuff.ErrUnauthorizedAddress
	}

	fmt.Println("          检查视图编号和块高度编号       ")
	// TODO: check view number and block height number检查视图编号和块高度编号

	return nil
}

func (ms *messageSet) addVerifiedMessage(msg *message) error {
	ms.messages[msg.Address] = msg
	return nil
}

func (ms *messageSet) String() string {
	ms.messagesMu.Lock()
	defer ms.messagesMu.Unlock()
	addresses := make([]string, 0, len(ms.messages))
	for _, v := range ms.messages {
		addresses = append(addresses, v.Address.String())
	}
	return fmt.Sprintf("[%v]", strings.Join(addresses, ", "))
}
