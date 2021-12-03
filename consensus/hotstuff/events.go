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

package hotstuff

// import (
// 	"github.com/ethereum/go-ethereum/common"
// )

// RequestEvent is posted to propose a proposal (posting the incoming block to/发布RequestEvent以提出建议（将传入的块发布到
// the main hotstuff engine anyway regardless of being the speaker or delegators) //不管是演讲者还是授权者，主要热门引擎都是如此）
type RequestEvent struct {
	Proposal Proposal
}

// MessageEvent is posted for HotStuff engine communication (posting the incoming/MessageEvent已发布用于HotStuff引擎通信（发布传入
// communication messages to the main hotstuff engine anyway)//与主hotstuff引擎的通信消息（无论如何）
type MessageEvent struct {
	Payload []byte
}

// SendingPubEvent is posted for HotStuff engine communication (posting the incoming为HotStuff引擎通信发布（发布传入
// communication messages to the main hotstuff engine anyway to broadcast the pub) 发送至主hotstuff引擎的通信消息（以广播酒吧）
type SendingPubEvent struct {
	Payload []byte
}

// FinalCommittedEvent is posted when a proposal is committed 提交提案时，将发布最终委员会
type FinalCommittedEvent struct {
}
