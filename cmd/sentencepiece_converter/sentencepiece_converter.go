package main

import (
	"encoding/hex"
	"fmt"
	"github.com/vikesh-raj/go-sentencepiece-encoder/sentencepiece"
	"github.com/wbrown/gpt_bpe"
	"google.golang.org/protobuf/proto"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"unicode"
)

var escaper *strings.Replacer

type DuplicateEntry struct {
	OldIdx int
	NewIdx int
	Repr   string
}

type VocabEntry struct {
	TokenId *gpt_bpe.Token
	Token   *string
	ByteId  *gpt_bpe.Token
	Byte    *string
}

type SentencePieceVocab struct {
	TokenToPiece []VocabEntry
	PieceToToken map[string]VocabEntry
}

func EscapeString(
	s string,
) (escaped string) {
	if escaper == nil {
		escaper = strings.NewReplacer(
			"\"", "\\\"",
			"\\", "\\\\",
			"\n", "\\n",
			"\r", "\\r",
			"\b", "\\b",
			"\t", "\\t")
	}
	escaped = escaper.Replace(s)
	asRunes := []rune(escaped)
	if len(asRunes) == 1 && (unicode.IsControl(asRunes[0]) ||
		!unicode.IsPrint(asRunes[0])) {
		escaped = fmt.Sprintf("\\u%04x", asRunes[0])
	}
	return escaped
}

func UnescapeString(
	s string,
) (unescaped string) {
	if strings.HasPrefix(s, "\\u") {
		// Unescape unicode
		code, _ := hex.DecodeString(s[2:6])
		unescaped = string(code)
		print(fmt.Sprintf("Unescaped unicode: %v -> %v", s, unescaped))
	} else {
		unescaped = s
	}
	return unescaped
}

func GenerateVocab(
	model *sentencepiece.ModelProto,
) (
	vocab *SentencePieceVocab,
	duplicates *[]DuplicateEntry,
	specials *[]string,
) {
	vocab = &SentencePieceVocab{
		TokenToPiece: make([]VocabEntry, len(model.GetPieces())+1),
		PieceToToken: make(map[string]VocabEntry),
	}
	specials = &[]string{}
	duplicateEntries := make([]DuplicateEntry, 0)
	duplicates = &duplicateEntries
	spaceReplacer := strings.NewReplacer(
		"▁", " ")
	// Build the vocab
	for pieceIdx, piece := range model.GetPieces() {
		repr := piece.GetPiece()
		pieceIsByte := piece.GetType() ==
			sentencepiece.ModelProto_SentencePiece_BYTE
		pieceIsControl := piece.GetType() ==
			sentencepiece.ModelProto_SentencePiece_CONTROL
		if pieceIsByte {
			hexRepr := piece.GetPiece()[3:5]
			encodedRepr, _ := hex.DecodeString(hexRepr)
			repr = string(encodedRepr)
		} else if pieceIsControl {
			*specials = append(*specials, repr)
		} else {
			repr = spaceReplacer.Replace(repr)
		}
		if dupeEntry, ok := vocab.PieceToToken[repr]; ok {
			var dupeIdx gpt_bpe.Token
			if dupeEntry.TokenId != nil {
				dupeIdx = *dupeEntry.TokenId
			} else {
				dupeIdx = *dupeEntry.ByteId
			}
			if pieceIsByte {
				byteToken := gpt_bpe.Token(pieceIdx)
				dupeEntry.Byte = &repr
				dupeEntry.ByteId = &byteToken
			} else {
				tokenToken := gpt_bpe.Token(pieceIdx)
				dupeEntry.Token = &repr
				dupeEntry.TokenId = &tokenToken
			}
			vocab.PieceToToken[repr] = dupeEntry
			vocab.TokenToPiece[dupeIdx] = dupeEntry
			vocab.TokenToPiece[gpt_bpe.Token(pieceIdx)] = dupeEntry
			print(fmt.Sprintf("Duplicate piece: old (%v): %v, dupe ("+
				"%v): %v\n",
				dupeIdx, model.GetPieces()[dupeIdx], pieceIdx, piece))
			*duplicates = append(*duplicates, DuplicateEntry{
				OldIdx: int(dupeIdx),
				NewIdx: pieceIdx,
				Repr:   repr,
			})
		} else {
			if pieceIsByte {
				byteToken := gpt_bpe.Token(pieceIdx)
				vocab.PieceToToken[repr] = VocabEntry{
					Byte:   &repr,
					ByteId: &byteToken,
				}
			} else {
				tokenToken := gpt_bpe.Token(pieceIdx)
				vocab.PieceToToken[repr] = VocabEntry{
					Token:   &repr,
					TokenId: &tokenToken,
				}
			}
			vocab.TokenToPiece[pieceIdx] = vocab.PieceToToken[repr]
		}
	}
	return vocab, duplicates, specials
}

func GenerateMergeTable(
	vocab *SentencePieceVocab,
) map[gpt_bpe.GPTPair]gpt_bpe.Token {
	mergeTable := make(map[gpt_bpe.GPTPair]gpt_bpe.Token, 0)

	// Loop over the model and print out the pieces
	currPair := gpt_bpe.GPTPair{"", ""}
	vocabSize := len(vocab.TokenToPiece)
	for leftTokenId := 0; leftTokenId < vocabSize; leftTokenId++ {
		leftToken := vocab.TokenToPiece[gpt_bpe.Token(leftTokenId)]
		if leftToken.Token == nil {
			continue
		}
		if *leftToken.Token == "" {
			continue
		} else {
			currPair.Left = *leftToken.Token
		}
		//print(fmt.Sprintf("Working on %v %v\n", leftToken, leftTokenId))
		for rightTokenId := 0; rightTokenId < vocabSize; rightTokenId++ {
			rightToken := vocab.TokenToPiece[gpt_bpe.Token(rightTokenId)]
			if rightToken.Token == nil {
				continue
			}
			if *rightToken.Token == "" {
				continue
			} else {
				currPair.Right = *rightToken.Token
			}
			if _, ok := mergeTable[currPair]; ok {
				continue
			}
			mergedToken := fmt.Sprintf("%v%v",
				currPair.Left,
				currPair.Right)
			if tokenEntry, ok := vocab.PieceToToken[mergedToken]; ok {
				tokenId := *tokenEntry.TokenId
				print(fmt.Sprintf("%v (%v) %v (%v) -> %v (%v)\n",
					currPair.Left, leftTokenId,
					currPair.Right, rightTokenId,
					mergedToken, tokenId))
				mergeTable[currPair] = tokenId
			}
		}
	}
	return mergeTable
}

// Our struct for the merge array
type MergeEntry struct {
	Left        string        `json:"left"`
	LeftToken   gpt_bpe.Token `json:"-"`
	Right       string        `json:"right"`
	RightToken  gpt_bpe.Token `json:"-"`
	Merged      string        `json:"-"`
	MergedToken gpt_bpe.Token `json:"-"`
}

func GenerateMergeEntries(
	vocab *SentencePieceVocab,
	mergeTable map[gpt_bpe.GPTPair]gpt_bpe.Token,
) []MergeEntry {
	// Turn the merge table into an array of entries
	mergeEntries := make([]MergeEntry, 0)
	for pair := range mergeTable {
		mergedToken := fmt.Sprintf("%v%v", pair.Left, pair.Right)
		// Skip single rune tokens
		if len([]rune(mergedToken)) == 1 {
			continue
		}
		mergeEntries = append(mergeEntries,
			MergeEntry{pair.Left,
				*vocab.PieceToToken[pair.Left].TokenId,
				pair.Right,
				*vocab.PieceToToken[pair.Right].TokenId,
				mergedToken,
				*vocab.PieceToToken[mergedToken].TokenId})
	}
	// Sort the merge array by token id
	sort.Slice(mergeEntries, func(i, j int) bool {
		return mergeEntries[i].MergedToken < mergeEntries[j].MergedToken
	})
	return mergeEntries
}

func WriteDuplicates(
	name string,
	duplicates *[]DuplicateEntry,
) {
	duplicatesFile, err := os.Create(fmt.Sprintf("%s.json", name))
	if err != nil {
		panic(err)
	}
	duplicatesFile.WriteString("[\n")
	for idx, dupe := range *duplicates {
		escaped := EscapeString(dupe.Repr)
		duplicatesFile.WriteString(fmt.Sprintf("  {\"old_id\": %v, "+
			"\"new_id\": %v, \"repr\": \"%v\"}",
			dupe.OldIdx, dupe.NewIdx, escaped))
		if idx != len(*duplicates)-1 {
			duplicatesFile.WriteString(",\n")
		} else {
			duplicatesFile.WriteString("\n")
		}
	}
	duplicatesFile.WriteString("]\n")
}

func WriteMergeFiles(
	name string,
	mergeEntries []MergeEntry,
	verbose bool,
) {
	mergesFile, err := os.Create(fmt.Sprintf("%s.json", name))
	if err != nil {
		panic(err)
	}

	if verbose {
		mergesFile.WriteString("[\n")
	} else {
		mergesFile.WriteString("[")
	}

	// Write the merge table to a text file and json file
	for idx, pair := range mergeEntries {
		leftRepr := EscapeString(pair.Left)
		rightRepr := EscapeString(pair.Right)
		mergedRepr := EscapeString(pair.Merged)

		if idx != 0 && verbose {
			mergesFile.WriteString(",\n  ")
		} else if idx != 0 {
			mergesFile.WriteString(",")
		}

		if verbose {
			mergesFile.WriteString(fmt.Sprintf(
				"{\"left\": \"%v\", \", left_token\": %v, "+
					"\"right\": \"%v\", \"right_token\": %v, "+
					"\"merged\": \"%v\", \"merged_token\": %v}",
				leftRepr, pair.LeftToken,
				rightRepr, pair.RightToken,
				mergedRepr, pair.MergedToken))
		} else {
			mergesFile.WriteString(fmt.Sprintf(
				"[\"%v\",\"%v\"]",
				leftRepr, rightRepr))
		}
	}
	if verbose {
		mergesFile.WriteString("]")
	} else {
		mergesFile.WriteString("\n]\n")
	}
	mergesFile.Close()
}

func WriteVocabFile(
	name string,
	vocab *SentencePieceVocab,
	verbose bool,
) {
	// Serialize vocab to a JSON file
	vocabFile, _ := os.Create(fmt.Sprintf("%s.json", name))
	vocabSize := len(vocab.TokenToPiece)

	var entryPrefix string
	if verbose {
		entryPrefix = " "
		vocabFile.WriteString("{\n")
	} else {
		entryPrefix = ""
		vocabFile.WriteString("{")
	}

	for tokenId := 0; tokenId < vocabSize; tokenId++ {
		tokenEntry := vocab.TokenToPiece[tokenId]
		var repr string
		if tokenEntry.TokenId != nil &&
			*tokenEntry.TokenId == gpt_bpe.Token(tokenId) {
			repr = EscapeString(*tokenEntry.Token)
		} else if tokenEntry.Byte != nil {
			// Convert our repr string to a byte
			reprByte := []byte(*tokenEntry.Byte)
			// Convert the byte to a hexstring
			repr = fmt.Sprintf("0x%02x", reprByte)
		}
		if tokenId != 0 && verbose {
			vocabFile.WriteString(",\n")
		} else if tokenId != 0 {
			vocabFile.WriteString(",")
		}

		vocabFile.WriteString(fmt.Sprintf("%s\"%v\":%s%d",
			entryPrefix, repr, entryPrefix, tokenId))
	}
	if verbose {
		vocabFile.WriteString("\n}\n")
	} else {
		vocabFile.WriteString("}")
	}
	vocabFile.Close()
}

func WriteSpecials(
	name string,
	specials *[]string,
) {
	specialsFile, err := os.Create(fmt.Sprintf("%s.txt", name))
	if err != nil {
		panic(err)
	}
	for _, special := range *specials {
		specialsFile.WriteString(fmt.Sprintf("%s\n", special))
	}
	specialsFile.Close()
}

func ConvertSentencepieceFiles(modelPath string) {
	bytes, err := ioutil.ReadFile(modelPath)
	if err != nil {
		print(fmt.Errorf("Unable to read file err %v", err))
	}
	var model sentencepiece.ModelProto
	err = proto.Unmarshal(bytes, &model)

	vocab, duplicates, specials := GenerateVocab(&model)
	WriteVocabFile("vocab", vocab, false)
	WriteDuplicates("duplicates", duplicates)
	WriteSpecials("specials", specials)
	mergeTable := GenerateMergeTable(vocab)
	mergeEntries := GenerateMergeEntries(vocab, mergeTable)
	WriteMergeFiles("merges", mergeEntries, false)
}

func main() {
	sp, err := sentencepiece.NewSentencepieceFromFile("../../"+
		"resources/data/nerdstash-tokenizer/v5.model", false)
	if err != nil {
		fmt.Println(err)
	}
	s := sentencepiece.NewEmptySentencepiece(false)
	ConvertSentencepieceFiles("../../" +
		"resources/data/nerdstash-tokenizer/nerdstash.model")
}
