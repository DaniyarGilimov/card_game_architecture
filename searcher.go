package gamearchitecture

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	gamemodel "github.com/daniyargilimov/card_game_model"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

const ( // RefresherTime Searcher timer for refresh
	RefresherTime = 3

	// PingPeriod Send pings to peer with this period. Must be less than pongWait.
	PingPeriod = (PongWait * 9) / 10

	// MaxMessageSize Maximum message size allowed from peer.
	MaxMessageSize = 512

	// WriteWait Time allowed to write a message to the peer.
	WriteWait = 4 * time.Second

	// PongWait Time allowed to read the next pong message from the peer.
	PongWait = 40 * time.Second
)

func WritePlayingInfo(rManager *RoomManager) {
	res, err := instPlayingInfo(rManager)
	if err != nil {
		log.Print(err.Error())
		return
	}

	rManager.Repo.WriteInfo("playInfo", res)
}

// SearcherRefresher used to refresh all searchers timely
func SearcherRefresher(rManager *RoomManager) {
	m := make(map[int64][]byte)
	// var ms5, ms20, ms100, ms1000, ms5000, ms10000, ms50000 []byte
	// ms5 = InstGetCertainListRoom(5, rManager)
	// ms20 = InstGetCertainListRoom(20, rManager)
	// ms100 = InstGetCertainListRoom(100, rManager)
	// ms1000 = InstGetCertainListRoom(1000, rManager)
	// ms5000 = InstGetCertainListRoom(5000, rManager)
	// ms10000 = InstGetCertainListRoom(10000, rManager)
	// ms50000 = InstGetCertainListRoom(50000, rManager)
	roomOptions, err := rManager.Services.GetRoomOptions()
	if err != nil {
		return
	}

	for _, v := range roomOptions.RoomOptions {
		m[v.Bet] = InstGetCertainListRoom(v.Bet, rManager)
	}

	rManager.SearcherLock.RLock()
	for _, sc := range rManager.AllSearcher {
		sc.Mu.RLock()

		if sc.State.Name == SearchingCertainState {
			msgc := m[sc.State.InitialBet]
			// switch sc.State.InitialBet {
			// case 5:
			// 	msgc = ms5
			// case 20:
			// 	msgc = ms20
			// case 100:
			// 	msgc = ms100
			// case 1000:
			// 	msgc = ms1000
			// case 5000:
			// 	msgc = ms5000
			// case 10000:
			// 	msgc = ms10000
			// case 50000:
			// 	msgc = ms50000
			// }
			go SendStateSearcher(sc, msgc)
		} else if sc.State.Name == SearchingAllState {
			msga := InstSendAllListRoom(rManager, sc.Chips)
			go SendStateSearcher(sc, msga)
		}
		sc.Mu.RUnlock()
	}
	rManager.SearcherLock.RUnlock()
	<-time.After(RefresherTime * time.Second)

	for {

		rManager.SearcherLock.RLock()
		for _, sc := range rManager.AllSearcher {
			sc.Mu.RLock()
			if sc.State.Name == SearchingAllState {
				msga := InstSendAllListRoom(rManager, sc.Chips) //instanciates default rooms
				go SendStateSearcher(sc, msga)
			} else if sc.State.Name == SearchingCertainState {
				res := InstGetCertainListRoom(sc.State.InitialBet, rManager)
				go SendStateSearcher(sc, res)
			}
			sc.Mu.RUnlock()
		}
		rManager.SearcherLock.RUnlock()
		<-time.After(RefresherTime * time.Second)
	}
}

// func TournamentEnd(tournamentID int, rManager *RoomManager) {
// 	for _, r := range rManager.allRooms {
// 		log.Print("room with tournament id: ", tournamentID, " room id: ", r.RoomData.RoomInfo.Name)
// 		if r.RoomData.TournamentID == tournamentID {
// 			log.Print("delete room with tournament id: ", tournamentID, " room id: ", r.RoomData.RoomInfo.Name)
// 			r.CloseEmergency <- "close"
// 		}
// 	}
// }

func SearcherCreate(ws *websocket.Conn, token string, playerId int, rManager *RoomManager) {
	rManager.SearcherLock.Lock()
	defer rManager.SearcherLock.Unlock()
	ss := &SearcherState{Name: SearchingStop}
	player, err := rManager.Repo.GetUserByID(playerId)
	if err != nil {
		logrus.Error(err)
		return
	}
	sc := &SearcherConn{
		WS:      ws,
		Ch:      make(chan []byte),
		Token:   token,
		UserID:  playerId,
		State:   ss,
		Chips:   player.Inventory.Chips,
		Service: rManager.Services,
	}

	sc.Mu = new(sync.RWMutex)
	if rManager.NumberOfSearchers > 100000 {
		rManager.NumberOfSearchers = 0
	}
	key := fmt.Sprint(rManager.NumberOfSearchers)
	sc.Key = key
	exists := true
	for exists {
		if _, exists = rManager.AllSearcher[key]; exists {
			rManager.NumberOfSearchers++
		}
	}
	rManager.AllSearcher[key] = sc
	rManager.NumberOfSearchers++
	go SearcherListener(sc, rManager)
	go SearcherWriter(sc, rManager)
}

func SearcherDelete(sc *SearcherConn, rManager *RoomManager) {
	rManager.SearcherLock.Lock()
	sc.WS.Close()
	close(sc.Ch)
	delete(rManager.AllSearcher, sc.Key)
	rManager.SearcherLock.Unlock()

}

// SendStateSearcher used to refresh a single searcher state
func SendStateSearcher(sc *SearcherConn, msg []byte) {
	defer func() {

		recover()
	}()
	sc.Ch <- msg
}

// SearcherListener used to listen searcher via websocket
func SearcherListener(sc *SearcherConn, roomManager *RoomManager) {
	defer func() {
		sc.WS.Close()
	}()

	sc.WS.SetReadLimit(MaxMessageSize)
	sc.WS.SetReadDeadline(time.Now().Add(PongWait))
	sc.WS.SetPongHandler(func(string) error { sc.WS.SetReadDeadline(time.Now().Add(PongWait)); return nil })

	for {
		_, command, err := sc.WS.ReadMessage()
		if err != nil {
			break
		}

		// Local struct to replace gamemodel.StatusInstruction
		type StatusInstruction struct {
			Status string `json:"status"`
			Data   struct {
				InitialBet int64 `json:"initialBet"`
				MinBet     int64 `json:"minBet"`
			} `json:"data"`
		}

		si := StatusInstruction{}

		json.Unmarshal(command, &si)
		z := make([]byte, len(command))
		copy(z, command)
		//log.Printf("SearcherListener: %s", string(z))
		switch si.Status {
		case "UTIL_CANCLE_INVITE":
			//go ser.CancleInvite(z, sc.UserID)
		case SearchRoomStop:
			sc.Mu.Lock()
			sc.State.Name = SearchingStop
			sc.Mu.Unlock()
		case SearchRoom:
		case "search_rooms":
			sc.Mu.Lock()
			sc.State.Name = SearchingCertainState
			sc.State.InitialBet = si.Data.InitialBet
			sc.Mu.Unlock()
			res := InstGetCertainListRoom(sc.State.InitialBet, roomManager)
			go SendStateSearcher(sc, res)
			// go sc.Service.ServiceLogging.CreateSearcherLog(sc.UserID, res)
		case SearchAllRooms:
		case "search_places":
			sc.Mu.Lock()
			sc.State.Name = SearchingAllState
			sc.Mu.Unlock()
			res := InstSendAllListRoom(roomManager, sc.Chips)
			go SendStateSearcher(sc, res)
			// go sc.Service.ServiceLogging.CreateSearcherLog(sc.UserID, res)
		case SearchPinCode:
			inst := PinCodeInstruction{}
			json.Unmarshal(z, &inst)
			sc.Mu.Lock()
			sc.State.Name = SearchingCertainState
			sc.Mu.Unlock()
			SendCertainListRoom(inst.Data.PinCode, sc, roomManager)
		default:
			log.Printf("Unexpected command is responsed it is: %s", string(command))
			goto Exit
		}
	}
Exit:
}

// SearcherWriter used to handle write to searcher
func SearcherWriter(sc *SearcherConn, roomManager *RoomManager) {
	ticker := time.NewTicker(PingPeriod)
	defer func() {
		ticker.Stop()
		SearcherDelete(sc, roomManager)
	}()
	for {
		select {
		case message, ok := <-sc.Ch:
			sc.WS.SetWriteDeadline(time.Now().Add(WriteWait))
			if !ok {
				sc.WS.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			sc.Mu.Lock()
			err := sc.WS.WriteMessage(websocket.TextMessage, message)
			sc.Mu.Unlock()
			if err != nil {
				return
			}
		case <-ticker.C:
			sc.WS.SetWriteDeadline(time.Now().Add(WriteWait))
			if err := sc.WS.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func SendCertainListRoom(pinCode string, sc *SearcherConn, rManager *RoomManager) {
	exportPinCode := PinCodeData{PinCode: pinCode, IsCorrect: false}
	exportData := &gamemodel.ExportData{Status: SearchPinCode}

	rManager.CloseRoomsLock.RLock()
	for _, r := range rManager.AllCloseRooms {
		if r.RoomInfo.Password == pinCode {
			exportPinCode.IsCorrect = true
			break
		}
	}

	rManager.CloseRoomsLock.RUnlock()

	exportData.Data = exportPinCode

	msg, err := json.Marshal(exportData)
	if err != nil {
		log.Print("ERROR: Cannot parse to allRooms to json format")
	}

	go SendStateSearcher(sc, msg)
}

// InstGetCertainListRoom used to get list of rooms with bet in bytes
func InstGetCertainListRoom(initialBet int64, rManager *RoomManager) []byte {
	exportRooms := []*gamemodel.ExportRoomData{}
	exportData := &gamemodel.ExportData{Status: SearchRoom, InstructionType: "search_rooms"}

	rManager.RoomsLock.RLock()
	for _, r := range rManager.AllRooms {
		if r.RoomInfo.InitialBet == initialBet {
			exportRooms = append(exportRooms, &gamemodel.ExportRoomData{
				ID:           r.ID,
				Title:        r.RoomInfo.Name,
				PlayersCount: len(r.PlayerConns),
				RoomSize:     r.RoomInfo.RoomSize,
				InitialBet:   r.RoomInfo.InitialBet,
				IsOpen:       r.RoomInfo.IsOpen,
				Password:     r.RoomInfo.Password,
			})
		}
	}

	rManager.RoomsLock.RUnlock()
	exportData.Data = exportRooms
	msg, err := json.Marshal(exportData)

	if err != nil {
		log.Print("ERROR: Cannot parse to allRooms to json format")
	}
	return msg
}

// instPlayingInfo used to create string of all players count in each bets, will be used to analyze which bet is played most
func instPlayingInfo(rManager *RoomManager) (string, error) {
	m := make(map[int64]int)

	m[0] = 0
	m[30] = 0
	m[300] = 0
	m[3000] = 0
	m[30000] = 0

	rManager.RoomsLock.RLock()
	for _, r := range rManager.AllRooms {
		//if r.RoomInfo.InitialBet == 20 || r.RoomInfo.InitialBet == 100 || r.RoomInfo.InitialBet == 1000 || r.RoomInfo.InitialBet == 10000 || r.RoomInfo.InitialBet == 50000 {
		m[r.RoomInfo.InitialBet] += len(r.PlayerConns)

		//}
	}
	for _, r := range rManager.AllCloseRooms {
		m[0] += len(r.PlayerConns)
	}
	rManager.RoomsLock.RUnlock()

	res := fmt.Sprintf("20 - %d \n 100 - %d \n 1000 - %d \n 10000 - %d \n 50000 - %d \n Close Rooms - %d", m[20], m[100], m[1000], m[10000], m[50000], m[0])

	return res, nil
}

// InstSendAllListRoom used to get list of rooms in bytes
func InstSendAllListRoom(rManager *RoomManager, chips int64) []byte {
	m := make(map[int64]int)

	roomOptions, err := rManager.Services.GetRoomOptions()
	if err != nil {
		return nil
	}
	// if chips != 0 {
	// 	if chips > 1000000 {
	// 		roomOptions.RoomOptions = roomOptions.RoomOptions[1:]
	// 	}
	// }
	for _, v := range roomOptions.RoomOptions {
		m[v.Bet] = 0
	}

	exportData := &gamemodel.ExportData{Status: SearchAllRooms, InstructionType: "search_places"}
	exportRooms := []*gamemodel.ExportAllRoomsData{}

	rManager.RoomsLock.RLock()
	for _, r := range rManager.AllRooms {
		for _, v := range roomOptions.RoomOptions {
			if r.RoomInfo.InitialBet == v.Bet {
				m[r.RoomInfo.InitialBet] += len(r.PlayerConns)
				break
			}
		}
	}

	for _, r := range rManager.AllCloseRooms {
		m[0] += len(r.PlayerConns)
	}
	rManager.RoomsLock.RUnlock()

	for _, v := range roomOptions.RoomOptions {
		exportRooms = append(exportRooms, &gamemodel.ExportAllRoomsData{InitialBet: v.Bet, PlayersAmount: m[v.Bet], PlaceName: v.PlaceName, Wallpaper: v.Wallpaper, PlayersImage: []string{}})
	}

	exportRooms = append(exportRooms, &gamemodel.ExportAllRoomsData{InitialBet: 0, PlayersAmount: m[0], PlaceName: "Close rooms", Wallpaper: "https://fivepokerdraw.com:4050/image/room/itemplaceclose.jpg", PlayersImage: []string{}, IsPrivate: true})

	exportData.Data = exportRooms
	msg, err := json.Marshal(exportData)
	if err != nil {
		logrus.Error("Cannot parse to allRooms to json format")
	}

	return msg
}
