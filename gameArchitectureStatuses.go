package gamearchitecture

const (
	RequestJoinOpen              = "JOIN_OPEN"
	RequestCreate                = "CREATE"
	RequestJoinPrivate           = "JOIN_PRIVATE"
	RequestJoinByID              = "JOIN_ROOM_ID"
	RequestJoinOpenAny           = "JOIN_OPEN_ANY"
	RequestJoinOpenOptions       = "JOIN_OPEN_OPTIONS"
	RequestJoinOpenAnyTournament = "JOIN_OPEN_ANY_TOURNAMENT"

	// SearchRoom status
	SearchRoom = "GN_SEARCHROOM"
	// SearchRoomStop status
	SearchRoomStop = "GN_SEARCHROOMSTOP"
	// SearchAllRooms status
	SearchAllRooms = "GN_SEARCHALL"
	SearchPinCode  = "GN_PINCODE"

	// SearchAllRoomsBot status
	SearchAllRoomsBot = "GN_SEARCHALLBOT"

	// SearchingAllState state
	SearchingAllState = "SEARCH_ALL"
	// SearchingCertainState state
	SearchingCertainState = "SEARCH_CERTAIN"
	// SearchingStop state
	SearchingStop = "STOP"
)
