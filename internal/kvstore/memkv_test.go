// Copyright 2019 Kaleido

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kvstore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/syndtr/goleveldb/leveldb/util"
)

func TestExerciseMockLDB(t *testing.T) {

	assert := assert.New(t)

	m := NewMockKV(nil)
	m.Put("test", []byte("val"))
	o2, _ := m.Get("test")
	assert.Equal("val", string(o2))
	m.Delete("test")
	_, err := m.Get("test")
	assert.Regexp("leveldb: not found", err)

	type myType struct {
		Value string
	}
	err = m.PutJSON("mykey", &myType{Value: "something"})
	assert.NoError(err)
	var retObj myType
	err = m.GetJSON("mykey", &retObj)
	assert.NoError(err)
	assert.Equal("something", retObj.Value)

	err = m.PutJSON("mykey", map[bool]bool{false: true})
	assert.Error(err)

	err = m.GetJSON("mykey", map[bool]bool{false: true})
	assert.Error(err)

	err = m.GetJSON("not found", map[bool]bool{false: true})
	assert.Equal(ErrorNotFound, err)

	assert.Nil(m.NewIterator())
	assert.Nil(m.NewIteratorWithRange(&util.Range{}))
	m.Close()

}

func TestMockKV(t *testing.T) {
	var kv KVStore
	kv = NewMockKV(nil)
	assert.NotNil(t, kv)
}
