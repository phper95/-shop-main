/**
* Copyright (C) 2020-2021
* All rights reserved, Designed By www.yixiang.co
* 注意：本软件为www.yixiang.co开发研制
 */
package wechat_menu_service

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"
	"shop/app/models"
	"shop/app/models/vo"
	menuDto "shop/app/service/wechat_menu_service/dto"
	"shop/pkg/constant"
	"shop/pkg/global"
)

type Menu struct {
	Id  int64
	Key string

	Dto menuDto.WechatMenu

	M *models.WechatMenu
}

func (d *Menu) GetAll() vo.ResultList {
	maps := make(map[string]interface{})
	maps["key"] = constant.WEICHAT_MENU

	data := models.GetWechatMenu(maps)
	return vo.ResultList{Content: data, TotalElements: 0}
}

func (d *Menu) Insert() error {
	button := gin.H{
		"button": d.Dto.Buttons,
	}
	jsonstr, _ := json.Marshal(button)
	str := string(jsonstr)
	global.LOG.Info(str)
	official := global.WechatOfficial
	m := official.GetMenu()
	err := m.SetMenuByJSON(str)
	if err != nil {
		global.LOG.Error(err)
	}

	result, _ := json.Marshal(d.Dto.Buttons)
	model := models.WechatMenu{
		Key:    constant.WEICHAT_MENU,
		Result: datatypes.JSON(result),
	}
	return models.UpdateByWechatMenu(&model)
}
