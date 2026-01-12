package gamearchitecture

import (
	"sync"

	"github.com/gorilla/websocket"
)

// SearcherConn is connection while searching
type SearcherConn struct {
	WS      *websocket.Conn
	Mu      *sync.RWMutex
	Ch      chan []byte    //Used to send data
	Done    chan struct{}  //Used to signal shutdown
	State   *SearcherState //Used to know searcher's state
	Key     string         //Used to find in all searchers
	Token   string         //Searchers token
	UserID  int            //Searchers userId
	Chips   int64
	Service Service
}

// SearcherState is searcher's state
type SearcherState struct {
	Name       string
	InitialBet int64
}

// PinCodeInstruction status
type PinCodeInstruction struct {
	Status string       `json:"status"`
	Data   *PinCodeData `json:"data"`
}

type PinCodeData struct {
	PinCode   string `json:"pinCode"`
	IsCorrect bool   `json:"isCorrect"`
}
