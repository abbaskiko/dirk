// Copyright © 2020 Attestant Limited.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package golang

import (
	"context"
	"fmt"

	"github.com/attestantio/dirk/rules"
	"github.com/attestantio/dirk/services/checker"
	"github.com/attestantio/dirk/services/ruler"
	"github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
)

// RunRules runs a number of rules and returns a result.
func (s *Service) RunRules(ctx context.Context,
	credentials *checker.Credentials,
	action string,
	rulesData []*ruler.RulesData,
) []rules.Result {
	span, ctx := opentracing.StartSpanFromContext(ctx, "ruler.golang.RunRules")
	defer span.Finish()

	// There must be some data.
	if len(rulesData) == 0 {
		log.Debug().Msg("Received no rules data entries")
		return []rules.Result{rules.FAILED}
	}
	results := make([]rules.Result, len(rulesData))
	for i := range rulesData {
		results[i] = rules.UNKNOWN
	}
	for i := range rulesData {
		if rulesData[i] == nil {
			log.Debug().Msg("Received nil rules data")
			results[i] = rules.FAILED
			return results
		}
		if rulesData[i].Data == nil {
			log.Debug().Msg("Received nil data in rules data")
			results[i] = rules.FAILED
			return results
		}
	}

	// Only some actions require locking.
	if action == ruler.ActionSign ||
		action == ruler.ActionSignBeaconProposal ||
		action == ruler.ActionSignBeaconAttestation {
		// We cannot allow multiple requests for the same public key.
		pubKeyMap := make(map[[48]byte]bool)
		for i := range rulesData {
			var key [48]byte
			if len(rulesData[i].PubKey) == 0 {
				log.Debug().Msg("Received no pubkey in rules data")
				results[i] = rules.FAILED
				return results
			}
			copy(key[:], rulesData[i].PubKey)
			if _, exists := pubKeyMap[key]; exists {
				log.Debug().Str("pubkey", fmt.Sprintf("%#x", rulesData[i].PubKey)).Msg("Multiple requests for same key")
				results[i] = rules.FAILED
				return results
			}
			pubKeyMap[key] = true
		}

		// Lock each public key as we come to it, to ensure that there can only be a single active rule
		// (and hence data update) for a given public key at any time.
		for i := range rulesData {
			var lockKey [48]byte
			copy(lockKey[:], rulesData[i].PubKey)
			s.locker.Lock(lockKey)
			defer s.locker.Unlock(lockKey)
		}
	}

	return s.runRules(ctx, credentials, action, rulesData)
}

// runRules runs a number of rules and returns a result.
// It assumes that validation checks have already been carried out against the data, and that
// suitable locks are held against the relevant public keys.
func (s *Service) runRules(ctx context.Context,
	credentials *checker.Credentials,
	action string,
	rulesData []*ruler.RulesData,
) []rules.Result {
	results := make([]rules.Result, len(rulesData))
	for i := range rulesData {
		results[i] = rules.UNKNOWN
	}

	for i := range rulesData {
		var name string
		if rulesData[i].AccountName == "" {
			name = rulesData[i].WalletName
		} else {
			name = fmt.Sprintf("%s/%s", rulesData[i].WalletName, rulesData[i].AccountName)
		}
		log := log.With().Str("account", name).Logger()

		metadata, err := s.assembleMetadata(ctx, credentials, rulesData[i].AccountName, rulesData[i].PubKey)
		if err != nil {
			log.Warn().Err(err).Msg("Failed to assemble metadata")
			results[i] = rules.FAILED
			continue
		}
		switch action {
		case ruler.ActionSign:
			rulesData, isExpectedType := rulesData[i].Data.(*rules.SignData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnSign(ctx, metadata, rulesData)
		case ruler.ActionSignBeaconProposal:
			reqData, isExpectedType := rulesData[i].Data.(*rules.SignBeaconProposalData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnSignBeaconProposal(ctx, metadata, reqData)
		case ruler.ActionSignBeaconAttestation:
			reqData, isExpectedType := rulesData[i].Data.(*rules.SignBeaconAttestationData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnSignBeaconAttestation(ctx, metadata, reqData)
		case ruler.ActionAccessAccount:
			reqData, isExpectedType := rulesData[i].Data.(*rules.AccessAccountData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnListAccounts(ctx, metadata, reqData)
		case ruler.ActionLockWallet:
			reqData, isExpectedType := rulesData[i].Data.(*rules.LockWalletData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnLockWallet(ctx, metadata, reqData)
		case ruler.ActionUnlockWallet:
			reqData, isExpectedType := rulesData[i].Data.(*rules.UnlockWalletData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnUnlockWallet(ctx, metadata, reqData)
		case ruler.ActionLockAccount:
			reqData, isExpectedType := rulesData[i].Data.(*rules.LockAccountData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnLockAccount(ctx, metadata, reqData)
		case ruler.ActionUnlockAccount:
			reqData, isExpectedType := rulesData[i].Data.(*rules.UnlockAccountData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnUnlockAccount(ctx, metadata, reqData)
		case ruler.ActionCreateAccount:
			reqData, isExpectedType := rulesData[i].Data.(*rules.CreateAccountData)
			if !isExpectedType {
				log.Warn().Msg("Data not of expected type")
				results[i] = rules.FAILED
				continue
			}
			results[i] = s.rules.OnCreateAccount(ctx, metadata, reqData)
		default:
			log.Warn().Str("action", action).Msg("Unknown action")
			results[i] = rules.FAILED
		}
		if results[i] == rules.UNKNOWN {
			log.Error().Msg("Unknown result from rule")
			results[i] = rules.FAILED
		}
	}

	return results
}

func (s *Service) assembleMetadata(ctx context.Context, credentials *checker.Credentials, accountName string, pubKey []byte) (*rules.ReqMetadata, error) {
	if credentials == nil {
		return nil, errors.New("no credentials")
	}

	// All requests must have a client.
	if credentials.Client == "" {
		return nil, errors.New("no client in credentials")
	}

	return &rules.ReqMetadata{
		Account: accountName,
		PubKey:  pubKey,
		IP:      credentials.IP,
		Client:  credentials.Client,
	}, nil
}
