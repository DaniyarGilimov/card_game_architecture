package gamearchitecture

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// CreateAndPopulateRoomWithBots creates a new room and populates it with a specified number of bots.
func CreateAndPopulateRoomWithBots(rManager *RoomManager, initialBet int64, roomNamePrefix string, isPublic bool, numBotsToCreate int, roomSize int, tournamentId int) (*Room, error) {
	if numBotsToCreate <= 0 || numBotsToCreate > roomSize {
		return nil, fmt.Errorf("invalid number of bots to create: %d (must be > 0 and <= roomSize %d)", numBotsToCreate, roomSize)
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
	fRoom := NewRoom(rManager, ri, ctx, cancel) // NewRoom uses ri.ID and ri.Name

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
	for i := 0; i < numBotsToCreate; i++ {
		botPlayerName := fmt.Sprintf("%sBot%d", roomNamePrefix, i+1)
		botPlayer := NewBotPlayer(rManager, botPlayerName, initialBet) // Give bots ample chips
		botAI := rManager.BotProvider.NewBot()

		go func(bPlayer *gamemodel.Player, bAI gamemodel.BotAI, rID int, rm *RoomManager) {
			if err := AddBotToRoom(rID, bPlayer, bAI, rm); err != nil {
				log.Printf("Bot System: Info - Could not add bot %s to room %d: %v", bPlayer.Name, rID, err)
			}
		}(botPlayer, botAI, fRoom.ID, rManager)
	}
	return fRoom, nil
}

// NewBotPlayer creates a new bot player instance.
// Bot IDs are negative to distinguish them from real players.
func NewBotPlayer(rManager *RoomManager, namePrefix string, initialChips int64) *gamemodel.Player {
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
		Name:     rManager.BotProvider.GetBotName(-playerID),
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
					Image: rManager.BotProvider.GetBotAvatarURL(-playerID),
				},
			},
			Level: model.PersonLevel{},
		},
		RuntimeData: &gamemodel.PlayerRuntimeData{},
	}
}

// AddBotToRoom adds a bot with a given AI to a specified room.
func AddBotToRoom(roomID int, botPlayer *gamemodel.Player, botAI gamemodel.BotAI, rManager *RoomManager) error {
	if botPlayer == nil || !botPlayer.IsBot || botAI == nil {
		return errors.New("invalid bot player or AI")
	}

	if TryMarkJoining(botPlayer.PlayerID, rManager) {
		return errors.New(fmt.Sprintf("bot %d already in join process", botPlayer.PlayerID))
	}

	defer ClearJoiningMark(botPlayer.PlayerID, rManager)

	var fRoom *Room
	rManager.RoomsLock.RLock()
	fRoom = rManager.AllRooms[roomID]
	rManager.RoomsLock.RUnlock()

	if fRoom == nil {
		rManager.CloseRoomsLock.RLock()
		fRoom = rManager.AllCloseRooms[roomID]
		rManager.CloseRoomsLock.RUnlock()
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

	Join(ctx, fRoom, botConn, rManager)

	return nil
}
