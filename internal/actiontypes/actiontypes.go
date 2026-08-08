// Package actiontypes owns the bounded action-type vocabulary shared by
// replica_cmds and split-client mempool records. Raw upstream strings must pass
// through Normalize before they may become metric labels.
package actiontypes

const Other = "other"

var known = map[string]struct{}{
	"approveAgent": {}, "approveBuilderFee": {},
	"agentEnableDexAbstraction": {}, "agentSendAsset": {}, "agentSetAbstraction": {},
	"batchModify": {}, "borrowLend": {}, "cancel": {}, "cancelByCloid": {},
	"claimRewards": {}, "evmRawTx": {}, "evmUserModify": {}, "modify": {},
	"multiSig": {}, "NetChildVaultPositionsAction": {}, "noop": {}, "order": {},
	"perpDeploy": {}, "scheduleCancel": {}, "sendAsset": {}, "sendToEvmWithData": {},
	"setReferrer": {}, "SetGlobalAction": {}, "spotDeploy": {}, "spotSend": {},
	"subAccountSpotTransfer": {}, "subAccountTransfer": {}, "tokenDelegate": {},
	"twapCancel": {}, "twapOrder": {}, "updateIsolatedMargin": {}, "updateLeverage": {},
	"usdClassTransfer": {}, "usdSend": {}, "userSetAbstraction": {}, "userDexAbstraction": {},
	"ValidatorSignWithdrawalAction": {}, "vaultTransfer": {},
	"VoteEthFinalizedWithdrawalAction": {}, "voteAppHash": {}, "withdraw3": {},

	// Current/retained replica_cmds vocabulary.
	"activateOutcomeDeployer": {}, "authorizeAqav2Role": {}, "CSignerAction": {},
	"CValidatorAction": {}, "deployerSendToEvmForFrozenUser": {}, "gossipPriorityBid": {},
	"hip3LiquidatorTransfer": {}, "l1ValidatorVoteBridgeDeposit": {}, "liquidate": {},
	"reassessFees": {}, "stakingLinkDisableTradingUser": {}, "startFeeTrial": {},
	"userOutcome": {}, "userPortfolioMargin": {}, "validatorL1Stream": {},
	"validatorL1UpdateReferenceOracle": {}, "validatorL1Vote": {}, "voteL1Hash": {},

	// Previously fixture-backed compatibility vocabulary.
	"cDeposit": {}, "convertToMultiSigUser": {}, "createSubAccount": {},
	"createVault": {}, "cWithdraw": {}, "evmUserSpotTransfer": {},
	"finalizeEvmContract": {}, "linkStakingUser": {}, "registerReferrer": {},
	"reserveRequestWeight": {}, "setDisplayName": {}, "spotUser": {},
	"subAccountModify": {}, "topUpIsolatedOnlyMargin": {}, "vaultDistribute": {},
	"vaultModify": {}, "VoteEthDepositAction": {}, "VoteGlobalAction": {},
}

// Normalize returns a fixed label and whether the raw value was allowlisted.
func Normalize(raw string) (string, bool) {
	if _, ok := known[raw]; ok {
		return raw, true
	}
	return Other, false
}

// Category maps a normalized action to a small, stable operational category.
func Category(action string) string {
	switch action {
	case "order", "twapOrder", "twapCancel", "cancel", "cancelByCloid",
		"batchModify", "modify", "scheduleCancel", "liquidate":
		return "trading"
	case "usdClassTransfer", "usdSend", "spotSend", "sendAsset", "agentSendAsset", "sendToEvmWithData",
		"vaultTransfer", "vaultDistribute", "subAccountTransfer", "subAccountSpotTransfer",
		"cDeposit", "cWithdraw", "withdraw3", "spotUser":
		return "transfer"
	case "approveAgent", "approveBuilderFee", "setReferrer", "updateLeverage",
		"updateIsolatedMargin", "createSubAccount", "subAccountModify", "registerReferrer",
		"linkStakingUser", "setDisplayName", "createVault", "vaultModify",
		"topUpIsolatedOnlyMargin", "convertToMultiSigUser", "multiSig",
		"agentEnableDexAbstraction", "agentSetAbstraction", "userSetAbstraction",
		"userDexAbstraction", "authorizeAqav2Role", "stakingLinkDisableTradingUser",
		"userPortfolioMargin":
		return "settings"
	case "tokenDelegate", "voteAppHash", "VoteEthDepositAction",
		"VoteEthFinalizedWithdrawalAction", "VoteGlobalAction", "SetGlobalAction",
		"CSignerAction", "CValidatorAction", "ValidatorSignWithdrawalAction",
		"NetChildVaultPositionsAction", "validatorL1UpdateReferenceOracle",
		"validatorL1Vote", "validatorL1Stream", "voteL1Hash",
		"l1ValidatorVoteBridgeDeposit", "gossipPriorityBid":
		return "governance"
	case "claimRewards", "reserveRequestWeight":
		return "rewards"
	case "evmRawTx", "evmUserModify", "evmUserSpotTransfer", "finalizeEvmContract":
		return "evm"
	case "perpDeploy", "spotDeploy", "activateOutcomeDeployer",
		"deployerSendToEvmForFrozenUser", "startFeeTrial", "reassessFees":
		return "deployment"
	case "borrowLend", "userOutcome", "hip3LiquidatorTransfer":
		return "markets"
	case "noop":
		return "system"
	default:
		return Other
	}
}
