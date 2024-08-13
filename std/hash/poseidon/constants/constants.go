package constants

import (
	"math/big"
)

// Number of full rounds
const RF = 8

// Generated by https://extgit.iaik.tugraz.at/krypto/hadeshash/-/blob/master/code/calc_round_numbers.py
// And rounded up to nearest integer that divides by t in [2, 13]
var RP = []int{56, 57, 56, 60, 60, 63, 64, 63, 60, 66, 60, 65}

// Round constants
var RC [][]*big.Int

// Maximum distance separable matrix
var MDS [][][]*big.Int

func init() {
	// Parameters are generated by a reference script https://extgit.iaik.tugraz.at/krypto/hadeshash/-/blob/master/code/generate_parameters_grain.sage
	// Used like so: sage generate_parameters_grain.sage 1 0 254 2 8 57 0x30644e72e131a029b85045b68181585d2833e84879b9709143e1f593f0000001
	constantsSTR := [][]string{constantsT2, constantsT3, constantsT4, constantsT5, constantsT6, constantsT7, constantsT8, constantsT9, constantsT10, constantsT11, constantsT12, constantsT13}
	mdsSTR := [][][]string{mdsT2, mdsT3, mdsT4, mdsT5, mdsT6, mdsT7, mdsT8, mdsT9, mdsT10, mdsT11, mdsT12, mdsT13}
	size := len(RP)
	RC = make([][]*big.Int, size)
	MDS = make([][][]*big.Int, size)

	for i := 0; i < size; i++ {
		// initialize round constants field elements
		RC[i] = make([]*big.Int, len(constantsSTR[i]))
		for j := 0; j < len(RC[i]); j++ {
			RC[i][j], _ = new(big.Int).SetString(constantsSTR[i][j], 16)
		}
		// initialize MDS matrix field elements
		MDS[i] = make([][]*big.Int, len(mdsSTR[i]))
		for j := 0; j < len(MDS[i]); j++ {
			MDS[i][j] = make([]*big.Int, len(mdsSTR[i][j]))
			for k := 0; k < len(MDS[i][j]); k++ {
				MDS[i][j][k], _ = new(big.Int).SetString(mdsSTR[i][j][k], 16)
			}
		}

	}
}
