package gamearchitecture

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	gamemodel "github.com/daniyargilimov/card_game_model"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type GameHandler struct {
	roomManager *RoomManager
	services    Service
}

func NewGameHandler(roomManager *RoomManager, services Service) *GameHandler {
	return &GameHandler{
		roomManager: roomManager,
		services:    services,
	}
}

// GeneralSocketHandler used to handle general socket
func (h *GameHandler) GeneralSocketHandler(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if _, ok := err.(websocket.HandshakeError); ok {
		log.Print("Not a websocket handshake")
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		return
	}

	request := &RequestGeneral{}
	if err := request.ParseRequest(r); err != nil {
		logrus.Warn(err.Error())
		ws.Close()
		return
	}
	playerID, err := h.services.ParseToken(request.PlayerToken)
	if playerID == 0 || err != nil {
		ws.Close()
		return
	}
	SearcherCreate(ws, request.PlayerToken, playerID, h.roomManager)
}

// RoomHandler used to handle room connection
func (h *GameHandler) RoomHandler(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if _, ok := err.(websocket.HandshakeError); ok {
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		return
	}
	request := &RequestJoinRoom{}
	if err := ParseRequest(request, r); err != nil {
		logrus.Warn(err.Error())
		ws.Close()
		return
	}

	switch request.JoinType {
	case RequestCreate:
		RoomCreate(request, ws, h.roomManager)
	case RequestJoinByID:
		RoomJoinByID(request.PlayerToken, request.RoomID, ws, h.roomManager)
	}
}

func (h *GameHandler) RoomHandlerV2(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if _, ok := err.(websocket.HandshakeError); ok {
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		return
	}
	request := &RequestJoinRoom{}
	if err := ParseRequest(request, r); err != nil {

		ws.Close()
		return
	}

	switch request.JoinType {
	case RequestCreate:
		RoomCreate(request, ws, h.roomManager)
	case RequestJoinPrivate:
		RoomJoinPrivateV2(request, ws, h.roomManager)
	case RequestJoinByID:
		RoomJoinByID(request.PlayerToken, request.RoomID, ws, h.roomManager)
	//case utils.RequestJoinOpenOptions:
	//	h.roomManager.RoomJoinOptionsV2(request, ws)
	case RequestJoinOpenAny:
		RoomJoinAny(request.PlayerToken, ws, h.roomManager)
	case RequestJoinOpenAnyTournament:
		RoomJoinAnyTournamentV2(request.PlayerToken, request, ws, h.roomManager)
	}
}

// RoomHandlerV3 final version
func (h *GameHandler) RoomHandlerV3(w http.ResponseWriter, r *http.Request) {
	log.Print("room handler v3 called")
	ws, err := upgrader.Upgrade(w, r, nil)
	if _, ok := err.(websocket.HandshakeError); ok {
		log.Print("Not a websocket handshake")
		http.Error(w, "Not a websocket handshake", 400)
		return
	} else if err != nil {
		log.Print("upgrade error: ", err.Error())
		return
	}
	request := &RequestJoinRoomV3{}
	if err := request.ParseRequest(r); err != nil {
		log.Print("parse request error: ", err.Error())
		ws.Close()
		return
	}

	request.RoomInfo.RoomSize = h.services.GetMaxRoomSize()

	log.Print("room handler v3 request type: ", request.Type)

	switch request.Type {
	case "join_private_room":
		RoomJoinByPasswordV3(request.PlayerToken, request.RoomInfo.Password, ws, h.roomManager)

	case "create_private_room":
		RoomCreatePrivateV3(&RequestJoinRoom{
			PlayerToken: request.PlayerToken,
			RoomInfo:    request.RoomInfo,
		}, ws, h.roomManager)

	case "create_room":
		RoomCreate(&RequestJoinRoom{
			PlayerToken: request.PlayerToken,
			RoomInfo:    request.RoomInfo,
		}, ws, h.roomManager)
	case "join_by_room_id":
		RoomJoinByID(request.PlayerToken, request.RoomID, ws, h.roomManager)
	case "join_any":
		RoomJoinAny(request.PlayerToken, ws, h.roomManager)
	case "join_tournament_with_bots":
		RoomJoinAnyTournamentWithBots(request.PlayerToken, request.RoomInfo.TournamentID, ws, h.roomManager)
	}
}

func (h *GameHandler) TournamentEndHandler(w http.ResponseWriter, r *http.Request) {

	tournament_id := strings.TrimPrefix(r.URL.Path, "/tournament_end/")

	if tournament_id != "" {
		tournamentID, err := strconv.Atoi(tournament_id)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		TournamentEnd(h.roomManager, tournamentID)
	}
	w.Write([]byte("OK"))
}

// ParseRequest
func ParseRequest(resp *RequestJoinRoom, r *http.Request) error {
	resp.RoomInfo = &gamemodel.RoomInfo{
		RoomSize: 6,
		IsOpen:   true,
	}

	params, _ := url.ParseQuery(r.URL.RawQuery)

	if len(params["token"]) > 0 {
		resp.PlayerToken = params["token"][0]
	} else {
		return errors.New("parse error | token undefined")
	}

	if len(params["join_type"]) > 0 {
		resp.JoinType = params["join_type"][0]
	} else {
		return errors.New("parse error")
	}

	switch resp.JoinType {
	case RequestCreate:
		initialBet, err1 := strconv.Atoi(params["initial_bet"][0])

		if initialBet < 5 {
			initialBet = 5
		}

		if err1 == nil {
			resp.RoomInfo.InitialBet = int64(initialBet)
		} else {
			return errors.New("parse error")
		}
		resp.RoomInfo.IsOpen = false
		resp.RoomInfo.RoomSize = 6
	case RequestJoinPrivate:
		if len(params["password"][0]) != 4 {
			return errors.New("bad request | password length incorrect")
		}
		resp.RoomInfo.Password = params["password"][0]
		resp.RoomInfo.RoomSize = 6
	case RequestJoinOpenOptions:
		if len(params["hand_chips"]) > 0 {
			handChips, err3 := strconv.Atoi(params["hand_chips"][0])
			if err3 != nil {
				return errors.New("parse error | hand chips not int")
			}
			resp.PlayerHandChips = int64(handChips)
		} else {
			return errors.New("parse error | hand chips undefined")
		}

		initialBet, err1 := strconv.Atoi(params["initial_bet"][0])
		if initialBet < 5 {
			initialBet = 5
		}

		if err1 == nil {
			resp.RoomInfo.InitialBet = int64(initialBet)
		} else {
			return errors.New("parse error")
		}
		resp.RoomInfo.RoomSize = 6
		resp.RoomInfo.IsOpen = true
	case RequestJoinByID:
		roomID, err1 := strconv.Atoi(params["room_id"][0])
		if err1 == nil {
			resp.RoomID = roomID
		} else {
			return errors.New("parse error")
		}
	case RequestJoinOpenAnyTournament:
		tournamentID, err1 := strconv.Atoi(params["tournament_id"][0])
		if err1 == nil {
			resp.TournamentID = tournamentID
		} else {
			return errors.New("parse error")
		}
	}

	if len(params["defined"]) > 0 {
		resp.RoomInfo.DefinedCards = true
	}

	return nil
}
