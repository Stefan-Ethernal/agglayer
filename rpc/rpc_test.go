package rpc

import (
	"errors"
	"math/big"
	"testing"

	"github.com/0xPolygon/agglayer/config"
	"github.com/0xPolygon/agglayer/interop"
	"github.com/0xPolygon/agglayer/mocks"

	agglayerTypes "github.com/0xPolygon/agglayer/rpc/types"
	"github.com/0xPolygonHermez/zkevm-node/ethtxmanager"
	validiumTypes "github.com/0xPolygonHermez/zkevm-node/jsonrpc/types"
	"github.com/0xPolygonHermez/zkevm-node/log"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/0xPolygon/agglayer/tx"
)

func TestInteropEndpointsGetTxStatus(t *testing.T) {
	t.Parallel()

	t.Run("BeginStateTransaction returns an error", func(t *testing.T) {
		t.Parallel()

		dbMock := mocks.NewDBMock(t)
		dbMock.On("BeginStateTransaction", mock.Anything).Return(nil, errors.New("error")).Once()

		cfg := &config.Config{}
		e := interop.New(
			log.WithFields("module", "test"),
			cfg,
			common.HexToAddress("0xadmin"),
			mocks.NewEthermanMock(t),
			mocks.NewEthTxManagerMock(t),
		)
		i := NewInteropEndpoints(e, dbMock, cfg)

		result, err := i.GetTxStatus(common.HexToHash("0xsomeTxHash"))

		require.Equal(t, "0x0", result)
		require.ErrorContains(t, err, "failed to begin dbTx")

		dbMock.AssertExpectations(t)
	})

	t.Run("failed to get tx", func(t *testing.T) {
		t.Parallel()

		txHash := common.HexToHash("0xsomeTxHash")

		txMock := new(mocks.TxMock)
		txMock.On("Rollback", mock.Anything).Return(nil).Once()

		dbMock := mocks.NewDBMock(t)
		dbMock.On("BeginStateTransaction", mock.Anything).Return(txMock, nil).Once()

		txManagerMock := mocks.NewEthTxManagerMock(t)
		txManagerMock.On("Result", mock.Anything, ethTxManOwner, txHash.Hex(), txMock).
			Return(ethtxmanager.MonitoredTxResult{}, errors.New("error")).Once()

		cfg := &config.Config{}
		e := interop.New(
			log.WithFields("module", "test"),
			cfg,
			common.HexToAddress("0xadmin"),
			mocks.NewEthermanMock(t),
			txManagerMock,
		)
		i := NewInteropEndpoints(e, dbMock, cfg)

		result, err := i.GetTxStatus(txHash)

		require.Equal(t, "0x0", result)
		require.ErrorContains(t, err, "failed to get tx")

		dbMock.AssertExpectations(t)
		txMock.AssertExpectations(t)
		txManagerMock.AssertExpectations(t)
	})

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()

		to := common.HexToAddress("0xreceiver")
		txHash := common.HexToHash("0xsomeTxHash")
		result := ethtxmanager.MonitoredTxResult{
			ID:     "1",
			Status: ethtxmanager.MonitoredTxStatusConfirmed,
			Txs: map[common.Hash]ethtxmanager.TxResult{
				txHash: {
					Tx: types.NewTransaction(1, to, big.NewInt(100_000), 21000, big.NewInt(10_000), nil),
				},
			},
		}

		txMock := new(mocks.TxMock)
		txMock.On("Rollback", mock.Anything).Return(nil).Once()

		dbMock := mocks.NewDBMock(t)
		dbMock.On("BeginStateTransaction", mock.Anything).Return(txMock, nil).Once()

		txManagerMock := mocks.NewEthTxManagerMock(t)
		txManagerMock.On("Result", mock.Anything, ethTxManOwner, txHash.Hex(), txMock).
			Return(result, nil).Once()

		cfg := &config.Config{}
		e := interop.New(
			log.WithFields("module", "test"),
			cfg,
			common.HexToAddress("0xadmin"),
			mocks.NewEthermanMock(t),
			txManagerMock,
		)
		i := NewInteropEndpoints(e, dbMock, cfg)

		status, err := i.GetTxStatus(txHash)

		require.NoError(t, err)
		require.Equal(t, "confirmed", status)

		dbMock.AssertExpectations(t)
		txMock.AssertExpectations(t)
		txManagerMock.AssertExpectations(t)
	})
}

func TestInteropEndpointsSendTx(t *testing.T) {
	t.Parallel()

	type testConfig struct {
		isL1ContractInMap   bool
		canBuildZKProof     bool
		isZKProofValid      bool
		isTxSigned          bool
		isAdminRetrieved    bool
		isSignerValid       bool
		canGetBatch         bool
		isBatchValid        bool
		isDbTxOpen          bool
		isTxAddedToEthTxMan bool
		isTxCommitted       bool

		expectedError string
	}

	testFn := func(cfg testConfig) {
		fullNodeRPCs := config.FullNodeRPCs{
			1: "someRPC",
		}
		tnx := tx.Tx{
			LastVerifiedBatch: agglayerTypes.ArgUint64(1),
			NewVerifiedBatch:  *agglayerTypes.ArgUint64Ptr(2),
			ZKP: tx.ZKP{
				NewStateRoot:     common.BigToHash(big.NewInt(11)),
				NewLocalExitRoot: common.BigToHash(big.NewInt(11)),
			},
			RollupID: 1,
		}
		signedTx := &tx.SignedTx{Tx: tnx}
		ethermanMock := mocks.NewEthermanMock(t)
		zkEVMClientCreatorMock := mocks.NewZkEVMClientClientCreatorMock(t)
		zkEVMClientMock := mocks.NewZkEVMClientMock(t)
		dbMock := mocks.NewDBMock(t)
		txMock := new(mocks.TxMock)
		ethTxManagerMock := mocks.NewEthTxManagerMock(t)

		executeTestFn := func() {
			c := &config.Config{
				FullNodeRPCs: fullNodeRPCs,
				L1:           config.L1Config{RollupManagerContract: common.HexToAddress("0xdeadbeef")},
			}

			e := interop.New(
				log.WithFields("module", "test"),
				c,
				common.HexToAddress("0xadmin"),
				ethermanMock,
				ethTxManagerMock,
			)
			i := NewInteropEndpoints(e, dbMock, c)
			i.executor.ZkEVMClientCreator = zkEVMClientCreatorMock

			result, err := i.SendTx(*signedTx)

			if cfg.expectedError != "" {
				require.Equal(t, "0x0", result)
				require.ErrorContains(t, err, cfg.expectedError)
			} else {
				require.NoError(t, err)
				require.Equal(t, signedTx.Tx.Hash(), result)
			}

			ethermanMock.AssertExpectations(t)
			zkEVMClientCreatorMock.AssertExpectations(t)
			zkEVMClientMock.AssertExpectations(t)
			dbMock.AssertExpectations(t)
			txMock.AssertExpectations(t)
			ethTxManagerMock.AssertExpectations(t)
		}

		if !cfg.isL1ContractInMap {
			fullNodeRPCs = config.FullNodeRPCs{}
			executeTestFn()

			return
		}

		if !cfg.canBuildZKProof {
			ethermanMock.On(
				"BuildTrustedVerifyBatchesTxData",
				uint64(tnx.LastVerifiedBatch),
				uint64(tnx.NewVerifiedBatch),
				mock.Anything,
				uint32(1),
			).Return(
				[]byte{},
				errors.New("error"),
			).Once()

			executeTestFn()

			return
		}

		ethermanMock.On(
			"BuildTrustedVerifyBatchesTxData",
			uint64(tnx.LastVerifiedBatch),
			uint64(tnx.NewVerifiedBatch),
			mock.Anything,
			uint32(1),
		).Return(
			[]byte{1, 2},
			nil,
		).Once()

		if !cfg.isZKProofValid {
			ethermanMock.On(
				"CallContract",
				mock.Anything,
				mock.Anything,
				mock.Anything,
			).Return(
				[]byte{},
				errors.New("error"),
			).Once()

			executeTestFn()

			return
		}

		ethermanMock.On(
			"CallContract",
			mock.Anything,
			mock.Anything,
			mock.Anything,
		).Return(
			[]byte{1, 2},
			nil,
		).Once()

		if !cfg.isTxSigned {
			executeTestFn()

			return
		}

		privateKey, err := crypto.GenerateKey()
		require.NoError(t, err)

		stx, err := tnx.Sign(privateKey)
		require.NoError(t, err)

		signedTx = stx

		if !cfg.isAdminRetrieved {
			ethermanMock.On(
				"GetSequencerAddr",
				uint32(1),
			).Return(
				common.Address{},
				errors.New("error"),
			).Once()

			executeTestFn()

			return
		}

		if !cfg.isSignerValid {
			ethermanMock.On(
				"GetSequencerAddr",
				uint32(1),
			).Return(
				common.BytesToAddress([]byte{1, 2, 3, 4}),
				nil,
			).Once()

			executeTestFn()

			return
		}

		ethermanMock.On(
			"GetSequencerAddr",
			uint32(1),
		).Return(
			crypto.PubkeyToAddress(privateKey.PublicKey),
			nil,
		).Once()

		zkEVMClientCreatorMock.On(
			"NewClient",
			mock.Anything,
		).Return(
			zkEVMClientMock,
		)

		if !cfg.canGetBatch {
			zkEVMClientMock.On(
				"BatchByNumber",
				mock.Anything,
				big.NewInt(int64(signedTx.Tx.NewVerifiedBatch)),
			).Return(
				nil,
				errors.New("error"),
			).Once()

			executeTestFn()

			return
		}

		if !cfg.isBatchValid {
			zkEVMClientMock.On(
				"BatchByNumber",
				mock.Anything,
				big.NewInt(int64(signedTx.Tx.NewVerifiedBatch)),
			).Return(
				&validiumTypes.Batch{
					StateRoot: common.BigToHash(big.NewInt(12)),
				},
				nil,
			).Once()

			executeTestFn()

			return
		}

		zkEVMClientMock.On(
			"BatchByNumber",
			mock.Anything,
			big.NewInt(int64(signedTx.Tx.NewVerifiedBatch)),
		).Return(
			&validiumTypes.Batch{
				StateRoot:     common.BigToHash(big.NewInt(11)),
				LocalExitRoot: common.BigToHash(big.NewInt(11)),
			},
			nil,
		).Once()

		if !cfg.isDbTxOpen {
			dbMock.On(
				"BeginStateTransaction",
				mock.Anything,
			).Return(
				nil,
				errors.New("error"),
			).Once()

			executeTestFn()

			return
		}

		dbMock.On(
			"BeginStateTransaction",
			mock.Anything,
		).Return(
			txMock,
			nil,
		).Once()

		if !cfg.isTxAddedToEthTxMan {
			ethTxManagerMock.On(
				"Add",
				mock.Anything,
				ethTxManOwner,
				signedTx.Tx.Hash().Hex(),
				mock.Anything,
				mock.Anything,
				mock.Anything,
				mock.Anything,
				mock.Anything,
				txMock,
			).Return(
				errors.New("error"),
			).Once()

			txMock.On(
				"Rollback",
				mock.Anything,
			).Return(
				nil,
			).Once()

			ethermanMock.On(
				"BuildTrustedVerifyBatchesTxData",
				uint64(tnx.LastVerifiedBatch),
				uint64(tnx.NewVerifiedBatch),
				mock.Anything,
				uint32(1),
			).Return(
				[]byte{1, 2},
				nil,
			).Once()

			executeTestFn()

			return
		}

		ethTxManagerMock.On(
			"Add",
			mock.Anything,
			ethTxManOwner,
			signedTx.Tx.Hash().Hex(),
			mock.Anything,
			mock.Anything,
			mock.Anything,
			mock.Anything,
			mock.Anything,
			txMock,
		).Return(
			nil,
		).Once()

		if !cfg.isTxCommitted {
			txMock.On(
				"Commit",
				mock.Anything,
			).Return(
				errors.New("error"),
			).Once()

			ethermanMock.On(
				"BuildTrustedVerifyBatchesTxData",
				uint64(tnx.LastVerifiedBatch),
				uint64(tnx.NewVerifiedBatch),
				mock.Anything,
				uint32(1),
			).Return(
				[]byte{1, 2},
				nil,
			).Once()

			executeTestFn()

			return
		}

		ethermanMock.On(
			"BuildTrustedVerifyBatchesTxData",
			uint64(tnx.LastVerifiedBatch),
			uint64(tnx.NewVerifiedBatch),
			mock.Anything,
			uint32(1),
		).Return(
			[]byte{1, 2},
			nil,
		).Once()

		txMock.On(
			"Commit",
			mock.Anything,
		).Return(
			nil,
		).Once()

		executeTestFn()
	}

	t.Run("don't have given contract in map", func(t *testing.T) {
		t.Parallel()

		testFn(testConfig{
			isL1ContractInMap: false,
			expectedError:     "there is no RPC registered",
		})
	})

	t.Run("could not build verified ZKP tx data", func(t *testing.T) {
		t.Parallel()

		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   false,
			expectedError:     "failed to build verify ZKP tx",
		})
	})

	t.Run("could not verified ZKP", func(t *testing.T) {
		t.Parallel()

		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    false,
			expectedError:     "failed to call verify ZKP response",
		})
	})

	t.Run("could not get signer", func(t *testing.T) {
		t.Parallel()

		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    true,
			isTxSigned:        false,
			expectedError:     "failed to get signer",
		})
	})

	t.Run("failed to get admin from L1", func(t *testing.T) {
		t.Parallel()

		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    true,
			isTxSigned:        true,
			isAdminRetrieved:  false,
			expectedError:     "failed to get admin from L1",
		})
	})

	t.Run("unexpected signer", func(t *testing.T) {
		t.Parallel()

		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    true,
			isTxSigned:        true,
			isAdminRetrieved:  true,
			isSignerValid:     false,
			expectedError:     "unexpected signer",
		})
	})

	t.Run("error on batch retrieval", func(t *testing.T) {
		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    true,
			isTxSigned:        true,
			isAdminRetrieved:  true,
			isSignerValid:     true,
			canGetBatch:       false,
			expectedError:     "failed to get batch from our node",
		})
	})

	t.Run("unexpected batch", func(t *testing.T) {
		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    true,
			isTxSigned:        true,
			isAdminRetrieved:  true,
			isSignerValid:     true,
			canGetBatch:       true,
			isBatchValid:      false,
			expectedError:     "Mismatch detected",
		})
	})

	t.Run("failed to begin dbTx", func(t *testing.T) {
		testFn(testConfig{
			isL1ContractInMap: true,
			canBuildZKProof:   true,
			isZKProofValid:    true,
			isTxSigned:        true,
			isAdminRetrieved:  true,
			isSignerValid:     true,
			canGetBatch:       true,
			isBatchValid:      true,
			isDbTxOpen:        false,
			expectedError:     "failed to begin dbTx",
		})
	})

	t.Run("failed to add tx to ethTxMan", func(t *testing.T) {
		testFn(testConfig{
			isL1ContractInMap:   true,
			canBuildZKProof:     true,
			isZKProofValid:      true,
			isTxSigned:          true,
			isAdminRetrieved:    true,
			isSignerValid:       true,
			canGetBatch:         true,
			isBatchValid:        true,
			isDbTxOpen:          true,
			isTxAddedToEthTxMan: false,
			expectedError:       "failed to add tx to ethTxMan",
		})
	})

	t.Run("failed to commit tx", func(t *testing.T) {
		testFn(testConfig{
			isL1ContractInMap:   true,
			canBuildZKProof:     true,
			isZKProofValid:      true,
			isTxSigned:          true,
			isAdminRetrieved:    true,
			isSignerValid:       true,
			canGetBatch:         true,
			isBatchValid:        true,
			isDbTxOpen:          true,
			isTxAddedToEthTxMan: true,
			isTxCommitted:       false,
			expectedError:       "failed to commit dbTx",
		})
	})

	t.Run("happy path", func(t *testing.T) {
		testFn(testConfig{
			isL1ContractInMap:   true,
			canBuildZKProof:     true,
			isZKProofValid:      true,
			isTxSigned:          true,
			isAdminRetrieved:    true,
			isSignerValid:       true,
			canGetBatch:         true,
			isBatchValid:        true,
			isDbTxOpen:          true,
			isTxAddedToEthTxMan: true,
			isTxCommitted:       true,
		})
	})
}
