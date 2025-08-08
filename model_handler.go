package gamearchitecture

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	gamemodel "github.com/daniyargilimov/card_game_model"
)

type RequestJoinRoom struct {
	JoinType        string // reconnect, join, create
	TournamentID    int
	PlayerToken     string
	RoomID          int
	PlayerHandChips int64 // chips to join
	RoomInfo        *gamemodel.RoomInfo
}

type RequestJoinRoomV3 struct {
	Type            string // reconnect, join, create
	PlayerToken     string
	RoomID          int
	PlayerHandChips int64 // chips to join
	RoomInfo        *gamemodel.RoomInfo
}

func (resp *RequestJoinRoomV3) ParseRequest(r *http.Request) error {
	resp.RoomInfo = &gamemodel.RoomInfo{
		RoomSize: 6,
		IsOpen:   true,
	}

	params, _ := url.ParseQuery(r.URL.RawQuery)

	if len(params["player_token"]) > 0 {
		resp.PlayerToken = params["player_token"][0]
	} else {
		return errors.New("parse error | token undefined")
	}

	if len(params["type"]) > 0 {
		resp.Type = params["type"][0]
	} else {
		return errors.New("parse error")
	}

	switch resp.Type {
	case "create_room":
		initialBet, err1 := strconv.Atoi(params["min_bet"][0])

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
	// case RequestJoinPrivate:
	// 	if len(params["password"][0]) != 4 {
	// 		return errors.New("bad request | password length incorrect")
	// 	}
	// 	resp.RoomInfo.Password = params["password"][0]
	// 	resp.RoomInfo.RoomSize = 6

	case "join_by_room_id":
		roomID, err1 := strconv.Atoi(params["room_id"][0])
		if err1 == nil {
			resp.RoomID = roomID
		} else {
			return errors.New("parse error")
		}

		if len(params["defined"]) > 0 {
			resp.RoomInfo.DefinedCards = true
		}

	}

	return nil
}

type RequestGeneral struct {
	PlayerToken string
}

func (resp *RequestGeneral) ParseRequest(r *http.Request) error {

	params, _ := url.ParseQuery(r.URL.RawQuery)

	if len(params["token"]) > 0 {
		resp.PlayerToken = params["token"][0]

		return nil
	}

	if len(params["player_token"]) > 0 {
		resp.PlayerToken = params["player_token"][0]

		return nil
	}

	return errors.New("not found there")
}
