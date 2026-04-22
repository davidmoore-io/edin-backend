package httpapi

// AllowedEDEventTypes is the set of Elite Dangerous journal event types accepted by the ingest API.
// Unknown types are rejected with 400. This is a denylist-by-default design.
var AllowedEDEventTypes = map[string]bool{
	// Core flight
	"FSDJump": true, "SupercruiseEntry": true, "SupercruiseExit": true,
	"FSDTarget": true, "StartJump": true, "Location": true, "Docked": true,
	"Undocked": true, "DockingRequested": true, "DockingGranted": true,
	"DockingDenied": true, "DockingCancelled": true, "DockingTimeout": true,
	"Touchdown": true, "Liftoff": true,
	// Exploration
	"Scan": true, "FSSDiscoveryScan": true, "FSSAllBodiesFound": true,
	"FSSBodySignals": true, "SAAScanComplete": true, "SAASignalsFound": true,
	"SellExplorationData": true, "MultiSellExplorationData": true,
	"Screenshot": true, "ApproachBody": true, "LeaveBody": true,
	"ApproachSettlement": true,
	// Trade
	"MarketBuy": true, "MarketSell": true, "Market": true,
	"BuyTradeData": true, "AsteroidCracked": true, "ProspectedAsteroid": true,
	"LaunchDrone": true, "MiningRefined": true, "CargoTransfer": true,
	"Cargo": true, "CollectCargo": true, "EjectCargo": true, "MissionCompleted": true,
	// Missions
	"MissionAccepted": true, "MissionFailed": true, "MissionAbandoned": true,
	"MissionRedirected": true, "Missions": true,
	// Combat
	"Interdicted": true, "Interdiction": true, "EscapeInterdiction": true,
	"PVPKill": true, "Died": true, "Resurrect": true,
	"ShieldState": true, "HullDamage": true, "UnderAttack": true,
	"FighterDestroyed": true, "FighterRebuilt": true, "LaunchFighter": true,
	"DockFighter": true, "VehicleSwitch": true, "LaunchSRV": true, "DockSRV": true,
	"SRVDestroyed": true,
	// Powerplay
	"PowerplayJoin": true, "PowerplayLeave": true, "PowerplayDefect": true,
	"PowerplaySalary": true, "PowerplayCollect": true, "PowerplayDeliver": true,
	"PowerplayFastTrack": true, "PowerplayVoucher": true,
	// Engineer / tech
	"EngineerProgress": true, "EngineerContribution": true, "EngineerCraft": true,
	"MaterialCollected": true, "MaterialDiscarded": true, "MaterialDiscovered": true,
	"Materials": true, "TechnologyBroker": true, "Synthesis": true,
	// Loadout / ship
	"Loadout": true, "LoadGame": true, "ShipyardBuy": true, "ShipyardSell": true,
	"ShipyardTransfer": true, "ShipyardSwap": true, "StoredShips": true,
	"ModuleInfo": true, "ModuleBuy": true, "ModuleSell": true, "ModuleStore": true,
	"ModuleRetrieve": true, "ModuleSellRemote": true, "MassModuleStore": true,
	"Outfitting": true, "StoredModules": true, "FetchRemoteModule": true,
	"SellShipOnRebuy": true,
	// Squadron
	"SquadronCreated": true, "SquadronStartup": true, "AppliedToSquadron": true,
	"JoinedSquadron": true, "LeftSquadron": true, "KickedFromSquadron": true,
	"InvitedToSquadron": true, "DisbandedSquadron": true, "SquadronDemotion": true,
	"SquadronPromotion": true,
	// Fleet carrier
	"CarrierJump": true, "CarrierBuy": true, "CarrierStats": true,
	"CarrierJumpRequest": true, "CarrierDepositFuel": true,
	"CarrierCrewServices": true, "CarrierFinance": true, "CarrierShipPack": true,
	"CarrierModulePack": true, "CarrierTradeOrder": true, "CarrierDockingPermission": true,
	"CarrierNameChange": true, "CarrierBankTransfer": true, "CarrierDecommission": true,
	"CarrierJumpCancelled": true,
	// Rank / progression
	"Rank": true, "Progress": true, "Reputation": true, "Promotion": true,
	"Statistics": true, "NpcCrewRank": true,
	// Odyssey (on-foot)
	"Disembark": true, "Embark": true, "BookDropship": true, "CancelDropship": true,
	"DropshipDeploy": true, "BackpackChange": true, "BuyMicroResources": true,
	"SellMicroResources": true, "TradeMicroResources": true, "CollectItems": true,
	"DropItems": true, "UseConsumable": true, "SuitLoadout": true,
	"SwitchSuitLoadout": true, "BuySuit": true, "SellSuit": true,
	"UpgradeSuit": true, "BuyWeapon": true, "SellWeapon": true, "UpgradeWeapon": true,
	"CreateSuitLoadout": true, "DeleteSuitLoadout": true, "RenameSuitLoadout": true,
	"LoadoutEquipModule": true, "LoadoutRemoveModule": true,
	"FCMaterials": true, "BookTaxi": true, "CancelTaxi": true,
	"ShutdownStarship": true, "RebootRepair": true,
	// Station services
	"CommunityGoal": true, "CommunityGoalJoin": true, "CommunityGoalReward": true,
	"CommunityGoalDiscard": true, "ColonisationConstructionDepot": true,
	"RedeemVoucher": true, "PayFines": true, "PayBounties": true,
	"BuyAmmo": true, "RepairAll": true, "Repair": true, "RefuelAll": true, "Refuel": true,
	"RestockVehicle": true, "ClearSavedGame": true, "NewCommander": true,
	"SetUserShipName": true, "Shipyard": true,
	// Misc / session
	"Fileheader": true, "Commander": true, "Music": true,
	"ReceiveText": true, "SendText": true,
	"WingAdd": true, "WingInvite": true, "WingJoin": true, "WingLeave": true,
	"Friends": true, "Shutdown": true,
}
