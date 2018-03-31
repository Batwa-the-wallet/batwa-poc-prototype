package blockchain

import "strconv"

func (this *Blockchain) getUnspentTxOut() {

}

func (this *Blockchain) getCorrespondingOutTx(wallet []byte, in *TxIn) *UnspentTxOut {
	walletStr := SanitizePubKey(wallet)

	outs, ok := this.unspentTxOut[walletStr]

	if !ok {
		return nil
	}

	for i, out := range outs {
		if out.InIdx == in.PrevIdx && compare(out.TxHash, in.PrevHash) == 0 {
			return &this.unspentTxOut[walletStr][i]
		}
	}

	return nil
}

func (this *Blockchain) UpdateUnspentTxOuts(block *Block) {
	for _, tx := range block.Transactions {
		hash := tx.Stamp.Hash

		own := false

		txValue := 0

		ownAddrStr := []byte(SanitizePubKey(this.wallets["main.key"].pub))

		addr := SanitizePubKey(tx.Stamp.Pub)
		if compare(tx.Stamp.Pub, this.wallets["main.key"].pub) == 0 {
			own = true
		}

		for _, in := range tx.Ins {
			out := this.getCorrespondingOutTx(tx.Stamp.Pub, &in)

			if out == nil {
				this.logger.Critical("WARNING !!!!! IMPOSSIBLE TO FIND UNSPENT TX OUT FROM APPARENTLY VALID BLOCK")

				return
			}

			this.RemoveUnspentOut(tx.Stamp.Pub, out)
		}

		for i, out := range tx.Outs {
			if own && compare(out.Address, ownAddrStr) != 0 {
				txValue -= out.Value
				addr = string(out.Address)
			}

			if !own && compare(out.Address, ownAddrStr) == 0 {
				txValue += out.Value
			}

			if len(tx.Ins) == 0 && len(tx.Outs) == 1 && compare(out.Address, ownAddrStr) == 0 {
				txValue += out.Value
				addr = "Miner fee (Block " + strconv.FormatInt(block.Header.Height, 10) + ")"
			}

			walletStr := string(out.Address)

			this.unspentTxOut[walletStr] = append(this.unspentTxOut[walletStr], UnspentTxOut{
				Out:    out,
				InIdx:  i,
				TxHash: hash,
			})
		}

		if txValue != 0 {
			this.history = append(this.history, HistoryTx{
				Address:   addr,
				Timestamp: block.Header.Timestamp,
				Amount:    txValue,
			})
		}
	}
}

func (this *Blockchain) RemoveUnspentOut(wallet []byte, out *UnspentTxOut) {
	walletStr := SanitizePubKey(wallet)
	idx := -1

	for i := range this.unspentTxOut[walletStr] {
		if &this.unspentTxOut[walletStr][i] == out {
			idx = i
		}
	}

	if idx == -1 {
		this.logger.Critical("WARNING !!!!! IMPOSSIBLE REMOVE UNSPENT TX OUT")

		return
	}

	this.unspentTxOut[walletStr] = append(this.unspentTxOut[walletStr][:idx], this.unspentTxOut[walletStr][idx+1:]...)
}

func (this *Blockchain) GetEnoughOwnUnspentOut(value int) []UnspentTxOut {
	walletStr := SanitizePubKey(this.wallets["main.key"].pub)

	var res []UnspentTxOut

	total := 0
	for _, unspent := range this.unspentTxOut[walletStr] {
		if unspent.IsTargeted {
			continue
		}

		total += unspent.Out.Value

		res = append(res, unspent)

		if total > value {
			break
		}
	}

	if total < value {
		return []UnspentTxOut{}
	}

	return res
}

func (this *Blockchain) GetAvailableFunds(wallet []byte) int {
	walletStr := SanitizePubKey(wallet)
	var total int

	total = 0

	// fmt.Println("Ouech", hex.EncodeToString(wallet.pub))
	for _, out := range this.unspentTxOut[walletStr] {
		total += out.Out.Value
	}

	return total
}

// Used to create a transaction without loss
func (this *Blockchain) GetInOutFromUnspent(value int, destWallet []byte, outs []UnspentTxOut) ([]TxIn, []TxOut) {
	insRes := []TxIn{}
	outsRes := []TxOut{}

	total := 0
	for _, out := range outs {
		insRes = append(insRes, TxIn{
			PrevHash: out.TxHash,
			PrevIdx:  out.InIdx,
		})

		total += out.Out.Value
	}

	outsRes = append(outsRes, TxOut{
		Value:   value,
		Address: destWallet,
	})

	if total > value {
		outsRes = append(outsRes, TxOut{
			Value:   total - value,
			Address: []byte(SanitizePubKey(this.wallets["main.key"].pub)),
		})
	}

	return insRes, outsRes
}
