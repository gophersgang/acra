// Copyright 2016, Cossack Labs Limited
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package keystore

import (
	"github.com/cossacklabs/acra/utils"
	"github.com/cossacklabs/themis/gothemis/keys"
)

const (
	DEFAULT_KEY_DIR_SHORT = ".acrakeys"
)

type KeyStore interface {
	GetZonePrivateKey(id []byte) (*keys.PrivateKey, error)
	HasZonePrivateKey(id []byte) bool
	GetProxyPublicKey(id []byte) (*keys.PublicKey, error)
	GetServerPrivateKey(id []byte) (*keys.PrivateKey, error)
	GetServerDecryptionPrivateKey(id []byte) (*keys.PrivateKey, error)
	// return id, public key, error
	GenerateZoneKey() ([]byte, []byte, error)

	GenerateProxyKeys(id []byte) error
	GenerateServerKeys(id []byte) error
	// generate key pair for data encryption/decryption
	GenerateDataEncryptionKeys(id []byte) error

	GetPoisonKeyPair() (*keys.Keypair, error)

	Reset()
}

func GetDefaultKeyDir() (string, error) {
	return utils.AbsPath(DEFAULT_KEY_DIR_SHORT)
}
