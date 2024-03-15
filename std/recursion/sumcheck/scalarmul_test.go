package sumcheck

import (
	"crypto/rand"
	"fmt"
	"math/big"
	stdbits "math/bits"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/secp256k1"
	fr_secp256k1 "github.com/consensys/gnark-crypto/ecc/secp256k1/fr"

	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs/scs"
	"github.com/consensys/gnark/std/algebra"
	"github.com/consensys/gnark/std/algebra/emulated/sw_emulated"
	"github.com/consensys/gnark/std/math/bits"
	"github.com/consensys/gnark/std/math/emulated"
	"github.com/consensys/gnark/std/math/emulated/emparams"
	"github.com/consensys/gnark/std/math/polynomial"
	"github.com/consensys/gnark/std/recursion"
	"github.com/consensys/gnark/test"
)

type ProjectivePoint[Base emulated.FieldParams] struct {
	X, Y, Z emulated.Element[Base]
}

type ScalarMulCircuit[Base, Scalars emulated.FieldParams] struct {
	Points  []sw_emulated.AffinePoint[Base]
	Scalars []emulated.Element[Scalars]

	nbScalarBits int
}

func (c *ScalarMulCircuit[B, S]) Define(api frontend.API) error {
	var fp B
	if len(c.Points) != len(c.Scalars) {
		return fmt.Errorf("len(inputs) != len(scalars)")
	}
	baseApi, err := emulated.NewField[B](api)
	if err != nil {
		return fmt.Errorf("new base field: %w", err)
	}
	scalarApi, err := emulated.NewField[S](api)
	if err != nil {
		return fmt.Errorf("new scalar field: %w", err)
	}
	poly, err := polynomial.New[B](api)
	if err != nil {
		return fmt.Errorf("new polynomial: %w", err)
	}
	// we use curve for marshaling points and scalars
	curve, err := algebra.GetCurve[S, sw_emulated.AffinePoint[B]](api)
	if err != nil {
		return fmt.Errorf("get curve: %w", err)
	}
	fs, err := recursion.NewTranscript(api, fp.Modulus(), []string{"alpha", "beta"})
	if err != nil {
		return fmt.Errorf("new transcript: %w", err)
	}
	// compute the all double-and-add steps for each scalar multiplication
	var results, accs []ProjectivePoint[B]
	for i := range c.Points {
		if err := fs.Bind("alpha", curve.MarshalScalar(c.Scalars[i])); err != nil {
			return fmt.Errorf("bind scalar %d alpha: %w", i, err)
		}
		if err := fs.Bind("alpha", curve.MarshalG1(c.Points[i])); err != nil {
			return fmt.Errorf("bind point %d alpha: %w", i, err)
		}
		result, acc, err := callHintScalarMulSteps[B, S](api, baseApi, scalarApi, c.nbScalarBits, c.Points[i], c.Scalars[i])
		if err != nil {
			return fmt.Errorf("hint scalar mul steps: %w", err)
		}
		results = append(results, result...)
		accs = append(accs, acc...)
	}
	// derive the randomness for random linear combination
	alphaNative, err := fs.ComputeChallenge("alpha")
	if err != nil {
		return fmt.Errorf("compute challenge alpha: %w", err)
	}
	alphaBts := bits.ToBinary(api, alphaNative, bits.WithNbDigits(fp.Modulus().BitLen()))
	alpha1 := baseApi.FromBits(alphaBts...)
	alpha2 := baseApi.Mul(alpha1, alpha1)
	alpha3 := baseApi.Mul(alpha1, alpha2)
	alpha4 := baseApi.Mul(alpha1, alpha3)
	alpha5 := baseApi.Mul(alpha1, alpha4)
	claimed := make([]*emulated.Element[B], len(results))
	// compute the random linear combinations of the intermediate results provided by the hint
	for i := range results {
		claimed[i] = baseApi.Sum(
			&accs[i].X,
			baseApi.MulNoReduce(alpha1, &accs[i].Y),
			baseApi.MulNoReduce(alpha2, &accs[i].Z),
			baseApi.MulNoReduce(alpha3, &results[i].X),
			baseApi.MulNoReduce(alpha4, &results[i].Y),
			baseApi.MulNoReduce(alpha5, &results[i].Z),
		)
	}
	// derive the randomness for folding
	betaNative, err := fs.ComputeChallenge("beta")
	if err != nil {
		return fmt.Errorf("compute challenge alpha: %w", err)
	}
	betaBts := bits.ToBinary(api, betaNative, bits.WithNbDigits(fp.Modulus().BitLen()))
	evalPoints := make([]*emulated.Element[B], stdbits.Len(uint(len(claimed)))-1)
	evalPoints[0] = baseApi.FromBits(betaBts...)
	for i := 1; i < len(evalPoints); i++ {
		evalPoints[i] = baseApi.Mul(evalPoints[i-1], evalPoints[0])
	}
	// compute the polynomial evaluation
	claimedPoly := polynomial.FromSliceReferences(claimed)
	claim, err := poly.EvalMultilinear(evalPoints, claimedPoly)
	if err != nil {
		return fmt.Errorf("eval multilinear: %w", err)
	}

	_ = claim

	return nil
}

func callHintScalarMulSteps[B, S emulated.FieldParams](api frontend.API,
	baseApi *emulated.Field[B], scalarApi *emulated.Field[S],
	nbScalarBits int,
	point sw_emulated.AffinePoint[B], scalar emulated.Element[S]) (results []ProjectivePoint[B], accumulators []ProjectivePoint[B], err error) {
	var fp B
	var fr S
	inputs := []frontend.Variable{fp.BitsPerLimb(), fp.NbLimbs()}
	inputs = append(inputs, baseApi.Modulus().Limbs...)
	inputs = append(inputs, point.X.Limbs...)
	inputs = append(inputs, point.Y.Limbs...)
	inputs = append(inputs, fr.BitsPerLimb(), fr.NbLimbs())
	inputs = append(inputs, scalarApi.Modulus().Limbs...)
	inputs = append(inputs, scalar.Limbs...)
	nbRes := nbScalarBits * int(fp.NbLimbs()) * 6
	hintRes, err := api.Compiler().NewHint(hintScalarMulSteps, nbRes, inputs...)
	if err != nil {
		return nil, nil, fmt.Errorf("new hint: %w", err)
	}
	res := make([]ProjectivePoint[B], nbScalarBits)
	acc := make([]ProjectivePoint[B], nbScalarBits)
	for i := range res {
		coords := make([]*emulated.Element[B], 6)
		for j := range coords {
			limbs := hintRes[i*(6*int(fp.NbLimbs()))+j*int(fp.NbLimbs()) : i*(6*int(fp.NbLimbs()))+(j+1)*int(fp.NbLimbs())]
			coords[j] = baseApi.NewElement(limbs)
		}
		res[i] = ProjectivePoint[B]{
			X: *coords[0],
			Y: *coords[1],
			Z: *coords[2],
		}
		acc[i] = ProjectivePoint[B]{
			X: *coords[3],
			Y: *coords[4],
			Z: *coords[5],
		}
	}
	return res, acc, nil
}

func hintScalarMulSteps(mod *big.Int, inputs []*big.Int, outputs []*big.Int) error {
	nbBits := int(inputs[0].Int64())
	nbLimbs := int(inputs[1].Int64())
	fpLimbs := inputs[2 : 2+nbLimbs]
	xLimbs := inputs[2+nbLimbs : 2+2*nbLimbs]
	yLimbs := inputs[2+2*nbLimbs : 2+3*nbLimbs]
	nbScalarBits := int(inputs[2+3*nbLimbs].Int64())
	nbScalarLimbs := int(inputs[3+3*nbLimbs].Int64())
	frLimbs := inputs[4+3*nbLimbs : 4+3*nbLimbs+nbScalarLimbs]
	scalarLimbs := inputs[4+3*nbLimbs+nbScalarLimbs : 4+3*nbLimbs+2*nbScalarLimbs]

	x := new(big.Int)
	y := new(big.Int)
	fp := new(big.Int)
	fr := new(big.Int)
	scalar := new(big.Int)
	if err := recompose(fpLimbs, uint(nbBits), fp); err != nil {
		return fmt.Errorf("recompose fp: %w", err)
	}
	if err := recompose(frLimbs, uint(nbScalarBits), fr); err != nil {
		return fmt.Errorf("recompose fr: %w", err)
	}
	if err := recompose(xLimbs, uint(nbBits), x); err != nil {
		return fmt.Errorf("recompose x: %w", err)
	}
	if err := recompose(yLimbs, uint(nbBits), y); err != nil {
		return fmt.Errorf("recompose y: %w", err)
	}
	if err := recompose(scalarLimbs, uint(nbScalarBits), scalar); err != nil {
		return fmt.Errorf("recompose scalar: %w", err)
	}

	scalarLength := len(outputs) / (6 * nbLimbs)
	accX := new(big.Int).Set(x)
	accY := new(big.Int).Set(y)
	accZ := big.NewInt(1)
	resultX := big.NewInt(0)
	resultY := big.NewInt(1)
	resultZ := big.NewInt(0)
	api := newBigIntEngine(fp)
	selector := new(big.Int)

	for i := 0; i < scalarLength; i++ {
		// selector := scalar.And()
		selector.And(scalar, big.NewInt(1))
		scalar.Rsh(scalar, 1)
		tmpX, tmpY, tmpZ := projAdd(api, accX, accY, accZ, resultX, resultY, resultZ)
		resultX, resultY, resultZ = projSelect(api, selector, tmpX, tmpY, tmpZ, resultX, resultY, resultZ)
		accX, accY, accZ = projDbl(api, accX, accY, accZ)
		if err := decompose(resultX, uint(nbBits), outputs[i*6*nbLimbs:i*6*nbLimbs+nbLimbs]); err != nil {
			return fmt.Errorf("decompose resultX: %w", err)
		}
		if err := decompose(resultY, uint(nbBits), outputs[i*6*nbLimbs+nbLimbs:i*6*nbLimbs+2*nbLimbs]); err != nil {
			return fmt.Errorf("decompose resultY: %w", err)
		}
		if err := decompose(resultZ, uint(nbBits), outputs[i*6*nbLimbs+2*nbLimbs:i*6*nbLimbs+3*nbLimbs]); err != nil {
			return fmt.Errorf("decompose resultZ: %w", err)
		}
		if err := decompose(accX, uint(nbBits), outputs[i*6*nbLimbs+3*nbLimbs:i*6*nbLimbs+4*nbLimbs]); err != nil {
			return fmt.Errorf("decompose accX: %w", err)
		}
		if err := decompose(accY, uint(nbBits), outputs[i*6*nbLimbs+4*nbLimbs:i*6*nbLimbs+5*nbLimbs]); err != nil {
			return fmt.Errorf("decompose accY: %w", err)
		}
		if err := decompose(accZ, uint(nbBits), outputs[i*6*nbLimbs+5*nbLimbs:(i+1)*6*nbLimbs]); err != nil {
			return fmt.Errorf("decompose accZ: %w", err)
		}
	}

	return nil
}

func recompose(inputs []*big.Int, nbBits uint, res *big.Int) error {
	if len(inputs) == 0 {
		return fmt.Errorf("zero length slice input")
	}
	if res == nil {
		return fmt.Errorf("result not initialized")
	}
	res.SetUint64(0)
	for i := range inputs {
		res.Lsh(res, nbBits)
		res.Add(res, inputs[len(inputs)-i-1])
	}
	// TODO @gbotrel mod reduce ?
	return nil
}

func decompose(input *big.Int, nbBits uint, res []*big.Int) error {
	// limb modulus
	if input.BitLen() > len(res)*int(nbBits) {
		return fmt.Errorf("decomposed integer does not fit into res")
	}
	for _, r := range res {
		if r == nil {
			return fmt.Errorf("result slice element uninitalized")
		}
	}
	base := new(big.Int).Lsh(big.NewInt(1), nbBits)
	tmp := new(big.Int).Set(input)
	for i := 0; i < len(res); i++ {
		res[i].Mod(tmp, base)
		tmp.Rsh(tmp, nbBits)
	}
	return nil
}

func TestScalarMul(t *testing.T) {
	assert := test.NewAssert(t)
	type B = emparams.Secp256k1Fp
	type S = emparams.Secp256k1Fr
	var P secp256k1.G1Affine
	var s fr_secp256k1.Element
	nbInputs := 1 << 2
	nbScalarBits := 256
	scalarBound := new(big.Int).Lsh(big.NewInt(1), uint(nbScalarBits))
	points := make([]sw_emulated.AffinePoint[B], nbInputs)
	scalars := make([]emulated.Element[S], nbInputs)
	for i := range points {
		P.ScalarMultiplicationBase(big.NewInt(1))
		s.SetRandom()
		P.ScalarMultiplicationBase(s.BigInt(new(big.Int)))
		sc, _ := rand.Int(rand.Reader, scalarBound)
		// t.Log(P.X.String(), P.Y.String(), sc.String())
		points[i] = sw_emulated.AffinePoint[B]{
			X: emulated.ValueOf[B](P.X),
			Y: emulated.ValueOf[B](P.Y),
		}
		scalars[i] = emulated.ValueOf[S](sc)
	}
	circuit := ScalarMulCircuit[B, S]{
		Points:       make([]sw_emulated.AffinePoint[B], nbInputs),
		Scalars:      make([]emulated.Element[S], nbInputs),
		nbScalarBits: nbScalarBits,
	}
	witness := ScalarMulCircuit[B, S]{
		Points:  points,
		Scalars: scalars,
	}
	err := test.IsSolved(&circuit, &witness, ecc.BLS12_377.ScalarField())
	assert.NoError(err)
	frontend.Compile(ecc.BLS12_377.ScalarField(), scs.NewBuilder, &circuit)
}
