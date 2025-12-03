package gamearchitecture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	gamemodel "github.com/daniyargilimov/card_game_model"

	model "github.com/daniyargilimov/card_api_model"
)

var botIDCounter int = 0  // Simple counter for unique bot IDs
var botIDMutex sync.Mutex // Mutex for botIDCounter

type BotProvider interface {
	NewBot() gamemodel.BotAI
	GetBotName(botID int) string
	GetBotAvatarURL(botID int) string
}

type BotConnectionController struct {
	// Leave when on someone leaves
	Leave chan *PlayerConn

	// Join when someone joins
	Join chan *PlayerConn

	rManager *RoomManager

	cancelBotTask context.CancelFunc // cancel pending bot add/remove

	targetBotsCount int // Desired number of bots in the room
}

func NewBotConnectionController(targetBotsCount int, rManager *RoomManager) *BotConnectionController {
	return &BotConnectionController{
		Leave:           make(chan *PlayerConn),
		Join:            make(chan *PlayerConn),
		rManager:        rManager,
		targetBotsCount: targetBotsCount,
	}
}

func (bcc *BotConnectionController) Run(roomCtx context.Context, room *Room) error {
	for {
		select {
		case _, ok := <-bcc.Leave:
			if !ok {
				return nil
			}

			bcc.handlePlayersCountChange(roomCtx, room)

		case _, ok := <-bcc.Join:
			if !ok {
				return nil
			}
			bcc.handlePlayersCountChange(roomCtx, room)

		case <-roomCtx.Done():
			return nil
		}
	}
}

func (bcc *BotConnectionController) handlePlayersCountChange(roomCtx context.Context, r *Room) {
	onlyBotsLeft := true
	r.PlayerConnsLock.RLock()
	count := len(r.PlayerConns)

	for _, v := range r.PlayerConns {
		if v.BotAI == nil {
			onlyBotsLeft = false
			break
		}
	}
	r.PlayerConnsLock.RUnlock()

	if onlyBotsLeft {
		goto removeAllTasks
	}

	// Priority case: Only 1 player → add bot fast
	if count == 1 {
		bcc.Schedule(roomCtx, random(2, 4), func() {
			bcc.AddBot(r.RoomInfo.InitialBet, r.ID)
		})
		return
	}

	// Below target (avg should be 4)
	if count < bcc.targetBotsCount {
		bcc.Schedule(roomCtx, random(10, 30), func() {
			bcc.AddBot(r.RoomInfo.InitialBet, r.ID)
		})
		return
	}

	// Above target
	if count > bcc.targetBotsCount {
		bcc.Schedule(roomCtx, random(10, 30), func() {
			bcc.RemoveBot(roomCtx, r)
		})
		return
	}

removeAllTasks:
	// Exactly 4 → no tasks needed or onlyBotsLeft
	if bcc.cancelBotTask != nil {
		bcc.cancelBotTask()
		bcc.cancelBotTask = nil
	}
}

func random(i1, i2 int) time.Duration {
	return time.Duration(i1+rand.Intn(i2-i1)) * time.Second
}

func (bcc *BotConnectionController) Schedule(roomCtx context.Context, duration time.Duration, task func()) {
	if bcc.cancelBotTask != nil {
		bcc.cancelBotTask()
	}

	ctx, cancel := context.WithCancel(context.Background())
	bcc.cancelBotTask = cancel

	go func() {
		select {
		case <-time.After(duration):
			task()
		case <-ctx.Done():
			return
		case <-roomCtx.Done():
			return
		}
	}()
}

// CreateAndPopulateRoomWithBots creates a new room and populates it with a specified number of bots.
func CreateAndPopulateRoomWithBots(rManager *RoomManager, initialBet int64, roomNamePrefix string, isPublic bool, initialBotsToCreate int, targetBotsCount int, roomSize int, tournamentId int) (*Room, error) {
	if initialBotsToCreate <= 0 || initialBotsToCreate > roomSize {
		return nil, fmt.Errorf("invalid number of bots to create: %d (must be > 0 and <= roomSize %d)", initialBotsToCreate, roomSize)
	}

	// 1. Construct RoomInfo
	ri := &gamemodel.RoomInfo{
		InitialBet:   initialBet,
		IsOpen:       isPublic,
		RoomSize:     roomSize,
		TournamentID: tournamentId,
		// Name:       fmt.Sprintf("%s (Bet: %d)", roomNamePrefix, initialBet),
	}

	if !isPublic { // Assign a password for private bot rooms if they are not open
		ri.Password = roomAssignPassword(rManager)
	}

	// 2. Create the Room
	mainContext := context.Background()
	ctx, cancel := context.WithCancel(mainContext)
	bcc := NewBotConnectionController(targetBotsCount, rManager)

	fRoom := NewRoom(rManager, ri, bcc, ctx, cancel) // NewRoom uses ri.ID and ri.Name

	// 3. Register the room
	if !fRoom.RoomInfo.IsOpen && fRoom.RoomInfo.Password != "" { // Private room with password
		rManager.CloseRoomsLock.Lock()
		rManager.AllCloseRooms[fRoom.ID] = fRoom
		rManager.CloseRoomsLock.Unlock()
	} else { // Public room
		rManager.RoomsLock.Lock()
		rManager.AllRooms[fRoom.ID] = fRoom
		rManager.RoomsLock.Unlock()
	}
	log.Printf("Bot System: Created room %d (%s) with InitialBet: %d, IsPublic: %t, Size: %d", fRoom.ID, fRoom.RoomInfo.Name, initialBet, isPublic, roomSize)

	// 4. Start the room's Run goroutine
	go func(roomToRun *Room) {
		if err := Run(roomToRun, rManager); err != nil {
			log.Printf("Bot System: Room %d (%s) closed with error: %v", roomToRun.ID, roomToRun.RoomInfo.Name, err)
		} else {
			log.Printf("Bot System: Room %d (%s) closed normally.", roomToRun.ID, roomToRun.RoomInfo.Name)
		}
		RoomDelete(roomToRun, rManager)
		roomToRun.ContextCancel() // Use the cancel func associated with the room's context
		RoomCloseChannels(roomToRun)
	}(fRoom)

	// 5. Add bots to the room
	for i := 0; i < initialBotsToCreate; i++ {
		bcc.AddBot(initialBet, fRoom.ID)
	}
	return fRoom, nil
}

func (bcc *BotConnectionController) AddBot(initialBet int64, rID int) error {
	botPlayer := bcc.NewBotPlayer(initialBet) // Give bots ample chips
	botAI := bcc.rManager.BotProvider.NewBot()

	go func(bPlayer *gamemodel.Player, bAI gamemodel.BotAI, rID int, rm *RoomManager) {
		if err := bcc.AddBotToRoom(rID, bPlayer, bAI); err != nil {
			log.Printf("Bot System: Info - Could not add bot %s to room %d: %v", bPlayer.Name, rID, err)
		}
	}(botPlayer, botAI, rID, bcc.rManager)
	return nil
}

func (bcc *BotConnectionController) RemoveBot(roomCtx context.Context, r *Room) {
	r.PlayerConnsLock.RLock()
	var botConn *PlayerConn
	for _, pc := range r.PlayerConns {
		if pc.BotAI != nil {
			botConn = pc
			break
		}
	}

	r.PlayerConnsLock.RUnlock()

	if botConn != nil {
		var inst gamemodel.PlayerLeftInstruction
		inst.Instruction = "BOT_CONNECTION_CONTROLLER_REMOVE"
		message, _ := json.Marshal(inst)

		select {
		case botConn.Ch <- message: // Signal bot to leave
			return
		case <-roomCtx.Done():
			return
		case <-time.After(2 * time.Second):
			return
		}
	}
}

// NewBotPlayer creates a new bot player instance.
// Bot IDs are negative to distinguish them from real players.
func (bcc *BotConnectionController) NewBotPlayer(initialChips int64) *gamemodel.Player {
	botIDMutex.Lock()
	botIDCounter-- // Ensure negative and unique IDs
	playerID := botIDCounter
	botIDMutex.Unlock()

	// Bots are given chips based on the initialBet of the room they might join.
	// Base amount is 50 times the initialChips.
	baseChips := initialChips * 50

	// Introduce a wider variation for bot chip amounts.
	// (playerID % 11) yields values in [-10, ..., -1, 0] for negative playerIDs.
	// Adding 5 shifts this to the range [-5, ..., +4, +5].
	// This creates 11 distinct multipliers for the variation.
	variationMultiplier := (playerID % 11) + 5
	variation := int64(variationMultiplier) * initialChips

	finalChips := baseChips + variation

	return &gamemodel.Player{
		Name:     bcc.rManager.BotProvider.GetBotName(-playerID),
		PlayerID: playerID,
		IsBot:    true,
		Inventory: &model.PersonInventory{ // Assuming model is accessible or define appropriately
			PlayerID:       playerID,
			Chips:          finalChips,
			Throwables:     []*model.ThrowableInventory{},
			Hats:           []*model.StaticInventory{},
			ActiveAvatarID: -1,
			Avatars: []*model.StaticInventory{
				{
					ID: -1,
					// Image: "https://api.dicebear.com/7.x/personas/png?seed=" + strconv.Itoa(playerID),
					Image: bcc.rManager.BotProvider.GetBotAvatarURL(-playerID),
				},
			},
			Level: model.PersonLevel{},
		},
		RuntimeData: &gamemodel.PlayerRuntimeData{},
	}
}

// AddBotToRoom adds a bot with a given AI to a specified room.
func (bcc *BotConnectionController) AddBotToRoom(roomID int, botPlayer *gamemodel.Player, botAI gamemodel.BotAI) error {
	if botPlayer == nil || !botPlayer.IsBot || botAI == nil {
		return errors.New("invalid bot player or AI")
	}

	if TryMarkJoining(botPlayer.PlayerID, bcc.rManager) {
		return errors.New(fmt.Sprintf("bot %d already in join process", botPlayer.PlayerID))
	}

	defer ClearJoiningMark(botPlayer.PlayerID, bcc.rManager)

	var fRoom *Room
	bcc.rManager.RoomsLock.RLock()
	fRoom = bcc.rManager.AllRooms[roomID]
	bcc.rManager.RoomsLock.RUnlock()

	if fRoom == nil {
		bcc.rManager.CloseRoomsLock.RLock()
		fRoom = bcc.rManager.AllCloseRooms[roomID]
		bcc.rManager.CloseRoomsLock.RUnlock()
		if fRoom == nil {
			return fmt.Errorf("room %d not found for bot %d", roomID, botPlayer.PlayerID)
		}
	}

	if botPlayer.Inventory.Chips < fRoom.RoomInfo.InitialBet*3 {
		return fmt.Errorf("bot %d has insufficient chips (%d) for room %d (bet %d)", botPlayer.PlayerID, botPlayer.Inventory.Chips, roomID, fRoom.RoomInfo.InitialBet)
	}

	fRoom.PlayerConnsLock.RLock()
	roomCount := len(fRoom.PlayerConns)
	fRoom.PlayerConnsLock.RUnlock()

	if fRoom.RoomInfo.RoomSize <= roomCount {
		return fmt.Errorf("room %d is full, cannot add bot %d", roomID, botPlayer.PlayerID)
	}

	botConn := NewBotPlayerConn(botPlayer, fRoom, botAI)
	if botConn == nil {
		return fmt.Errorf("failed to create bot connection for bot %d in room %d", botPlayer.PlayerID, roomID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	Join(ctx, fRoom, botConn, bcc.rManager)

	return nil
}
