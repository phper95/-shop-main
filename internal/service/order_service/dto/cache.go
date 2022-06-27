package dto

import (
	"shop/internal/service/cart_service/vo"
)

type Cache struct {
	CartInfo   []vo.Cart  `json:"cartInfo"`
	PriceGroup PriceGroup `json:"priceGroup"`
	Other      Other      `json:"other"`
}
