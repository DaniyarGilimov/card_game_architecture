package gamearchitecture

import (
	"context"
	"sync"
	"time"

	gamemodel "github.com/daniyargilimov/card_game_model"

	model "github.com/daniyargilimov/card_api_model"

	"github.com/gorilla/websocket"
)

type RoomManager struct {
	Services Service
	Repo     Repo

	AllRooms  map[int]*Room
	RoomsLock sync.RWMutex

	AllCloseRooms  map[int]*Room
	CloseRoomsLock sync.RWMutex

	AllSearcher           map[string]*SearcherConn
	SearcherLock          sync.RWMutex
	RoomsCount            int
	JoiningPlayersMutex   sync.RWMutex
	PlayersAttemptingJoin map[int]bool

	// Round-robin room tracking
	UserVisitedRooms     map[int]map[int]bool // userID -> roomID -> visited
	UserVisitedRoomsLock sync.RWMutex

	// Testing Purpose
	MutexLock         sync.RWMutex
	MutexLocked       map[string]string
	NumberOfSearchers int

	NewGame     gamemodel.GameFactory
	BotProvider BotProvider
	// NewBot  gamemodel.BotFactory
}

type Service interface {
	ParseToken(accessToken string) (int, error)
	GetUserByToken(token string) (*model.User, error)
	GetAnyInitialBet(userChips, ltBet int64) (int64, int64, error)
	SendDelete(message string)
	GetRoomOptions() (*model.RoomOptions, error)

	CreateSearcherLog(playerId int, msg []byte)
	CreatePlayerLog(playerId int, playerChips int64, msg []byte, roomId int, tournamentId int, playerIds []int)
	CreatePlayerSentLog(playerId int, playerChips int64, msg []byte, roomId int, tournamentId int, playerIds []int)

	GetMaxRoomSize() int

	gamemodel.Service
}

type Repo interface {
	GetUserByID(userID int) (*model.User, error) // TODO: After creating module with api models
	GetTournamentChips(userID, tournamentID int) (int64, error)
}

// Room is a room
type Room struct {
	Ctx           context.Context
	ContextCancel context.CancelFunc

	ID                      int
	Service                 Service
	RoomInfo                *gamemodel.RoomInfo
	Game                    gamemodel.Game
	BotConnectionController *BotConnectionController

	// ConnectionOrder []int
	PlayerConns     []*PlayerConn
	PlayerConnsLock sync.RWMutex // Lock for Room's PlayerConns list

	//JoinLock sync.Mutex //used to lock join, to fix concurrent 2 players join

	// Register requests from the connections.
	Join chan *PlayerConn

	// Unregister requests from connections.
	Leave chan *PlayerConn

	// BroadcastChannel used to broadcast all messages
	BroadcastChannel chan []byte

	// BroadcastExceptChannel used to broadcast to all except one player
	BroadcastExceptChannel chan (gamemodel.UserIdAndByte)

	// UnicastChannel used to send message to one player
	UnicastChannel chan (gamemodel.UserIdAndByte)

	// PingChannel used to ping if room is not in deadlock
	PingChannel chan int
}

// PlayerConn is Player's connection
type PlayerConn struct {
	Player *gamemodel.Player

	Token string `json:"-"`
	// UserID       int    `json:"userId"`
	WS           *websocket.Conn
	LastActivity time.Time
	CleanupOnce  sync.Once
	Ch           chan []byte   // Used to send data (to WebSocket or BotAI)
	Done         chan struct{} // Signal channel for shutdown

	BotAI gamemodel.BotAI // Holds the AI logic for the bot

	Mu *sync.Mutex
}
