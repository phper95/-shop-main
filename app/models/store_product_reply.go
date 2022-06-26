/**
* Copyright (C) 2020-2021
* All rights reserved, Designed By www.yixiang.co
* 注意：本软件为www.yixiang.co开发研制
 */
package models

import "time"

type StoreProductReply struct {
	Uid                  int64     `json:"uid"`
	ProductId            int64     `json:"product_id"`
	Oid                  int64     `json:"oid"`
	Unique               string    `json:"unique"`
	ReplyType            string    `json:"reply_type"`
	ProductScore         int       `json:"product_score"`
	ServiceScore         int       `json:"service_score"`
	Comment              string    `json:"comment"`
	Pics                 string    `json:"pics"`
	MerchantReplyContent string    `json:"merchant_reply_content"`
	MerchantReplyTime    time.Time `json:"merchant_reply_time"`
	IsReply              int8      `json:"is_reply"`
	BaseModel
}

func (StoreProductReply) TableName() string {
	return "store_product_reply"
}

// get all
func GetAllProductReply(pageNUm int, pageSize int, maps interface{}) (int64, []StoreProductReply) {
	var (
		total int64
		data  []StoreProductReply
	)
	db.Model(&StoreProductReply{}).Where(maps).Count(&total)
	db.Where(maps).Offset(pageNUm).Limit(pageSize).Order("id desc").Find(&data)

	return total, data
}

func AddStoreProductReply(m *StoreProductReply) error {
	var err error
	if err = db.Create(m).Error; err != nil {
		return err
	}

	return err
}

func UpdateByStoreProductReply(m *StoreProductReply) error {
	var err error
	err = db.Save(m).Error
	if err != nil {
		return err
	}

	return err
}

func DelByStoreProductReply(ids []int64) error {
	var err error
	err = db.Where("id in (?)", ids).Delete(&StoreProductReply{}).Error
	if err != nil {
		return err
	}

	return err
}
